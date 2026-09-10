import { FieldType } from '@grafana/data';
import type { PluginExtensionPanelContext } from '@grafana/data';

import { summarizePanelData } from './panelData';

// Minimal stand-in for the context Grafana hands a panel-menu extension: only
// the properties the summarizer actually reads.
const panelWith = (series: unknown[]): Readonly<PluginExtensionPanelContext> =>
    ({
        title: 'CPU',
        timeZone: 'utc',
        data: { series },
    } as unknown as Readonly<PluginExtensionPanelContext>);

const timeSeries = (values: number[], name = 'value') => ({
    name: 'A-series',
    fields: [
        {
            name: 'Time',
            type: FieldType.time,
            config: {},
            values: values.map((_, i) => 1700000000000 + i * 60000),
        },
        { name, type: FieldType.number, config: {}, labels: { instance: 'web-1' }, values },
    ],
});

describe('summarizePanelData', () => {
    it('reports stats and points from the data the panel already has', () => {
        const summary = summarizePanelData(panelWith([timeSeries([1, 2, 3])]));

        expect(summary).toContain('min=1 max=3 avg=2 last=3');
        // Labels stand in for the legend name, which is what the user sees.
        expect(summary).toContain('value{instance="web-1"}');
        // Timestamps are formatted, not raw epoch millis.
        expect(summary).toContain('=1');
        expect(summary).not.toContain('1700000000000');
    });

    it('downsamples long series but keeps the last point', () => {
        const values = Array.from({ length: 500 }, (_, i) => i);
        const summary = summarizePanelData(panelWith([timeSeries(values)]), {
            maxSeries: 10,
            maxPointsPerSeries: 5,
        });

        // The end of the series is the reason a panel gets opened: it must
        // survive the sampling.
        expect(summary).toContain('=499');
        expect(summary).toContain('last=499');
        // 5 requested points, plus the forced final one. Counted on the
        // values line (the one right after the series header), since the
        // stats and timestamp lines carry numbers of their own.
        const lines = (summary ?? '').split('\n');
        const valuesLine = lines[lines.findIndex((line) => line.startsWith('- ')) + 1] ?? '';
        expect(valuesLine.trim().split(/\s+/)).toHaveLength(6);
        // Timestamps are printed once for the frame, not next to every value:
        // on a multi-series panel that duplication doubles the token cost for
        // nothing.
        expect(lines.filter((line) => line.includes('timestamps'))).toHaveLength(1);
    });

    it('says so when the panel has no data rather than inventing an empty series', () => {
        expect(summarizePanelData(panelWith([]))).toBeUndefined();
    });

    it('survives the read-only proxy Grafana wraps the context in', () => {
        // Grafana's proxy throws on properties whose getter caches into the
        // field (field.state and friends). One booby-trapped frame must not
        // cost the others, nor break opening the chat.
        const hostile = {
            name: 'hostile',
            get fields(): never {
                throw new TypeError('proxy set handler returned false for property "state"');
            },
        };
        const summary = summarizePanelData(panelWith([hostile, timeSeries([7, 8])]));

        expect(summary).toContain('min=7 max=8');
    });

    it('reports query errors alongside the values', () => {
        const context = {
            title: 'CPU',
            timeZone: 'utc',
            data: { series: [timeSeries([1])], errors: [{ message: 'upstream timeout' }] },
        } as unknown as Readonly<PluginExtensionPanelContext>;

        expect(summarizePanelData(context)).toContain('upstream timeout');
    });
});
