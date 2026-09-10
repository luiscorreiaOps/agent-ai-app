import { dateTimeFormat, FieldType } from '@grafana/data';
import type { DataFrame, Field, PanelData, PluginExtensionPanelContext } from '@grafana/data';

/**
 * Turns the data a panel has ALREADY loaded into a compact text block for the
 * model.
 *
 * Grafana hands the extension `context.data` -- the very frames the user is
 * looking at, fetched by the dashboard itself. Without this, the model had no
 * values at all and its only way to say anything concrete was to re-run the
 * panel's query through a tool: a second execution of a query that had just
 * run, an extra agent round (so the whole conversation resent in prompt
 * tokens), and a result that can legitimately differ from what is on screen
 * (different instant, different step, template variables the tool call has to
 * guess). The data is right there; send it.
 *
 * Tools stay available and useful -- comparing against last week, widening
 * the range, correlating with logs all still need them. What disappears is
 * the round trip that only re-fetched what the panel already had.
 */

/**
 * How much of the panel to send.
 *
 * Every token here is paid on every round of the agent loop, so this is a
 * deliberate trade, not a "send everything" default: enough shape for the
 * model to describe a trend, spot an outlier and read the current value,
 * without turning a 500-point series into thousands of tokens.
 */
export interface PanelDataBudget {
    maxSeries: number;
    maxPointsPerSeries: number;
}

export const DEFAULT_PANEL_DATA_BUDGET: PanelDataBudget = {
    maxSeries: 10,
    maxPointsPerSeries: 20,
};

/**
 * Defensive read of anything reached through the panel context.
 *
 * Grafana exposes that context behind a read-only proxy that wraps nested
 * objects recursively. Some properties trigger lazy computation that writes
 * back into the field (`field.state`) -- the write is refused and throws. The
 * exact set of booby-trapped properties is undocumented and version
 * dependent, so nothing here is read without a fallback: a panel we cannot
 * summarize must still open the chat.
 */
function safe<T>(read: () => T, fallback: T): T {
    try {
        return read();
    } catch {
        return fallback;
    }
}

/**
 * Downsamples while keeping both ends: one point every `step`, plus the last
 * one, so the end of the series -- usually the reason the panel was opened --
 * is never the part that gets cut.
 */
function sample<T>(values: T[], max: number): T[] {
    if (values.length <= max) {
        return values;
    }
    const step = Math.ceil(values.length / max);
    const out: T[] = [];
    for (let i = 0; i < values.length; i += step) {
        out.push(values[i]);
    }
    if (out[out.length - 1] !== values[values.length - 1]) {
        out.push(values[values.length - 1]);
    }
    return out;
}

function formatNumber(value: unknown): string {
    if (typeof value !== 'number' || !Number.isFinite(value)) {
        return 'null';
    }
    // 4 significant digits: past that we are paying tokens for noise.
    return String(Number(value.toPrecision(4)));
}

/**
 * Stats computed by hand, on purpose.
 *
 * `reduceField` and `getFieldDisplayName` from @grafana/data cache their
 * result into `field.state`, i.e. they write to the field -- which the
 * read-only proxy above rejects with a TypeError. So nothing from that helper
 * family is used here.
 */
function describeStats(field: Field): string {
    let min = Number.POSITIVE_INFINITY;
    let max = Number.NEGATIVE_INFINITY;
    let sum = 0;
    let count = 0;
    let last: number | undefined;

    for (const raw of safe(() => field.values, [] as unknown[])) {
        if (typeof raw !== 'number' || !Number.isFinite(raw)) {
            continue;
        }
        min = Math.min(min, raw);
        max = Math.max(max, raw);
        sum += raw;
        count += 1;
        last = raw;
    }

    if (count === 0) {
        return 'no numeric value';
    }

    return [
        `min=${formatNumber(min)}`,
        `max=${formatNumber(max)}`,
        `avg=${formatNumber(sum / count)}`,
        `last=${formatNumber(last)}`,
    ].join(' ');
}

/**
 * Display name rebuilt from inert properties only -- see describeStats for
 * why getFieldDisplayName is not an option. `config` carries the legend
 * format the datasource resolved, which is exactly the name the user sees in
 * the legend, so it is also the most meaningful one for the model.
 */
function displayName(field: Field, frame: DataFrame): string {
    const name = safe(() => String(field.name), 'series');

    return safe(() => {
        const config = field.config;
        if (config?.displayNameFromDS) {
            return config.displayNameFromDS;
        }
        if (config?.displayName) {
            return config.displayName;
        }

        const entries = field.labels ? Object.entries(field.labels) : [];
        if (entries.length) {
            return `${name}{${entries.map(([k, v]) => `${k}="${String(v)}"`).join(', ')}}`;
        }

        const frameName = frame.name;
        return frameName ? `${frameName} ${name}` : name;
    }, name);
}

function summarizeFrame(frame: DataFrame, timeZone: string, budget: PanelDataBudget): string[] {
    const fields = safe(() => frame.fields, [] as Field[]);
    const timeField = fields.find((f) => safe(() => f.type, undefined) === FieldType.time);
    const valueFields = fields.filter((f) => safe(() => f.type, undefined) === FieldType.number);
    if (!valueFields.length) {
        return [];
    }

    // One set of sample indexes for the whole frame, so every series lines up
    // with the timestamps printed once above them. Emitting a timestamp next
    // to every single value instead costs roughly as many tokens as the
    // values themselves, for information that is identical on every series.
    const length = Math.max(
        ...valueFields.map((f) => safe(() => f.values.length, 0)),
        safe(() => timeField?.values.length ?? 0, 0)
    );
    const indexes = sample(
        Array.from({ length }, (_, i) => i),
        budget.maxPointsPerSeries
    );

    const lines: string[] = [];
    if (timeField) {
        // No space inside a timestamp: the whole line is whitespace-separated
        // values, and Grafana's default format ("YYYY-MM-DD HH:mm:ss") would
        // make every point look like two.
        const times = indexes.map((i) =>
            safe(() => dateTimeFormat(timeField.values[i], { timeZone, format: 'YYYY-MM-DDTHH:mm:ss' }), '?')
        );
        lines.push(`  timestamps (${timeZone}): ${times.join(' ')}`);
    }

    for (const field of valueFields.slice(0, budget.maxSeries)) {
        lines.push(`- ${displayName(field, frame)} [${describeStats(field)}]`);
        const values = safe(() => field.values, [] as unknown[]);
        lines.push(`  ${indexes.map((i) => formatNumber(values[i])).join(' ')}`);
    }

    if (valueFields.length > budget.maxSeries) {
        lines.push(`- (${valueFields.length - budget.maxSeries} more series not included)`);
    }

    return lines;
}

/**
 * Flattens the panel's loaded frames to text, or returns undefined when there
 * is nothing worth sending (no frames, or only non-numeric ones -- a table or
 * a log panel, where the query itself is the better context and the model can
 * still reach for a tool).
 */
export function summarizePanelData(
    panelContext: Readonly<PluginExtensionPanelContext>,
    budget: PanelDataBudget = DEFAULT_PANEL_DATA_BUDGET
): string | undefined {
    return safe(() => buildSummary(panelContext, budget), undefined);
}

function buildSummary(
    panelContext: Readonly<PluginExtensionPanelContext>,
    budget: PanelDataBudget
): string | undefined {
    const data: PanelData | undefined = panelContext.data;
    const frames = safe(() => data?.series ?? [], [] as DataFrame[]);
    const timeZone = safe(() => panelContext.timeZone, 'browser');
    const lines: string[] = [];

    for (const frame of frames) {
        // One unreadable frame must not cost us the others.
        lines.push(...safe(() => summarizeFrame(frame, timeZone, budget), ['- (unreadable series)']));
    }

    const errors = safe(() => data?.errors?.map((e) => e.message).filter(Boolean) ?? [], [] as Array<string | undefined>);
    if (!lines.length && !errors.length) {
        return undefined;
    }

    const sections = lines.length
        ? [`Values currently displayed in the panel (downsampled to ${budget.maxPointsPerSeries} points per series):`, ...lines]
        : ['The panel is showing no data over this time range.'];

    if (errors.length) {
        sections.push('Query errors:', ...errors.map((message) => `  ${message}`));
    }

    return sections.join('\n');
}
