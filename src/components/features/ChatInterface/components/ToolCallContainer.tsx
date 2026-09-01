import React, { useState, useMemo, useRef, useEffect } from 'react';
import { GrafanaTheme2 } from '@grafana/data';
import { useStyles2, Icon } from '@grafana/ui';
import { css } from '@emotion/css';
import { ToolExecution } from '../../../../types/llm.types';
import { brand } from '../../../../brand';

interface ToolCallContainerProps {
    toolExecutions: ToolExecution[];
    theme: GrafanaTheme2;
    isStreaming?: boolean;
}

// Compute error indices from tool executions
const getErrorIndices = (toolExecutions: ToolExecution[]): Set<number> => {
    const errorIndices = new Set<number>();
    toolExecutions.forEach((exec, index) => {
        if (exec.status === 'error') {
            errorIndices.add(index);
        }
    });
    return errorIndices;
};

// The visual distinction between a Grafana-local tool call and an
// internet-backed search must be driven by backend-provided metadata (kind)
// or the well-known tool name -- never by text the LLM generated -- so a
// prompt-injected response can't fake a "local only" indicator while data
// actually left the cluster.
const isInternetSearch = (exec: ToolExecution): boolean =>
    exec.kind === 'internet_search' || exec.name === 'search_web';

// Fields under which tools carry a query. Surfacing it avoids making anyone
// read a JSON blob to find the one thing they came to check.
const QUERY_FIELDS = ['expr', 'query', 'rawSql', 'logql', 'promql'];

interface ParsedArguments {
    query?: string;
    rest?: string;
    raw?: string;
}

// Arguments arrive as JSON produced by the model, so they can be malformed.
// Fall back to the raw text rather than hiding the information.
const parseArguments = (args?: string): ParsedArguments | null => {
    if (!args || args.trim() === '' || args.trim() === '{}') {
        return null;
    }
    try {
        const parsed = JSON.parse(args) as Record<string, unknown>;
        const queryField = QUERY_FIELDS.find((f) => typeof parsed[f] === 'string' && parsed[f]);
        const query = queryField ? String(parsed[queryField]) : undefined;
        const others = Object.fromEntries(Object.entries(parsed).filter(([k]) => k !== queryField));
        return {
            query,
            rest: Object.keys(others).length > 0 ? JSON.stringify(others, null, 2) : undefined,
        };
    } catch {
        return { raw: args };
    }
};

export const ToolCallContainer: React.FC<ToolCallContainerProps> = ({ toolExecutions, theme, isStreaming = false }) => {
    // Track which items user has manually collapsed
    const manuallyCollapsed = useRef<Set<number>>(new Set());
    const styles = useStyles2(getStyles);

    // Compute expanded items: all errors minus manually collapsed
    const errorIndices = useMemo(() => getErrorIndices(toolExecutions), [toolExecutions]);
    const [expandedItems, setExpandedItems] = useState<Set<number>>(() => errorIndices);

    // Sync expanded items when new errors appear
    const prevErrorIndicesRef = useRef<Set<number>>(errorIndices);
    useEffect(() => {
        const prevErrors = prevErrorIndicesRef.current;
        const newErrors = new Set<number>();
        errorIndices.forEach(idx => {
            if (!prevErrors.has(idx)) {
                newErrors.add(idx);
            }
        });
        if (newErrors.size > 0) {
            setExpandedItems(prev => {
                const next = new Set(prev);
                newErrors.forEach(idx => {
                    if (!manuallyCollapsed.current.has(idx)) {
                        next.add(idx);
                    }
                });
                return next;
            });
        }
        prevErrorIndicesRef.current = errorIndices;
    }, [errorIndices]);

    const toggleExpand = (index: number) => {
        setExpandedItems(prev => {
            const next = new Set(prev);
            if (next.has(index)) {
                next.delete(index);
            } else {
                next.add(index);
            }
            return next;
        });
    };

    const [isAllExpanded, setIsAllExpanded] = useState(false);

    if (!toolExecutions || toolExecutions.length === 0) {
        return null;
    }

    const hasAnyError = toolExecutions.some(exec => exec.status === 'error');
    // We consider it pending if the whole chat is still streaming OR if any individual tool is explicitly pending
    const showSpinner = isStreaming || toolExecutions.some(exec => exec.status === 'pending');

    const internetSearchCount = toolExecutions.filter(isInternetSearch).length;
    const onlyInternetSearch = internetSearchCount > 0 && internetSearchCount === toolExecutions.length;
    const grafanaToolCount = toolExecutions.length - internetSearchCount;

    const summaryText = showSpinner
        ? onlyInternetSearch
            ? 'Searching the web...'
            : internetSearchCount > 0
                ? 'Searching the web and using Grafana tools...'
                : 'Using tools...'
        : onlyInternetSearch
            ? 'Searched the web'
            : internetSearchCount > 0
                ? `Searched the web and used ${grafanaToolCount} Grafana tool${grafanaToolCount === 1 ? '' : 's'}`
                : `Used ${toolExecutions.length} tool${toolExecutions.length === 1 ? '' : 's'}`;

    return (
        <div className={styles.containerWrapper}>
            <div
                className={styles.summaryHeader}
                onClick={() => setIsAllExpanded(!isAllExpanded)}
            >
                <div className={styles.toolCallStatus}>
                    {showSpinner ? (
                        <Icon name="fa fa-spinner" className={styles.toolCallSpinner} />
                    ) : hasAnyError ? (
                        <span className={styles.toolCallError}>✗</span>
                    ) : (
                        <span className={styles.toolCallSuccess}>✓</span>
                    )}
                </div>
                <span className={styles.summaryText}>
                    {summaryText}
                </span>
                <Icon name={isAllExpanded ? 'angle-down' : 'angle-right'} size="sm" />
            </div>

            {isAllExpanded && (
                <div className={styles.toolCallsWrapper}>
                    {toolExecutions.map((exec, index) => {
                        const isExpanded = expandedItems.has(index);
                        const hasError = exec.status === 'error';
                        const internetSearch = isInternetSearch(exec);
                        const iconName = internetSearch ? 'search' : 'plug';
                        const displayName = exec.label || (internetSearch ? 'Internet search' : exec.name);
                        const statusLabel =
                            exec.status === 'pending'
                                ? exec.statusLabel || displayName
                                : exec.status === 'success'
                                    ? exec.doneLabel || displayName
                                    : displayName;
                        // statusLabel is "Using Grafana tools..." / "Used
                        // Grafana tool" for EVERY Grafana tool, so a list of
                        // five calls rendered five identical rows. Appending
                        // the function name is what tells them apart. Internet
                        // search keeps its label alone: "Internet search"
                        // already says it, and its technical name adds nothing.
                        const displayStatus = internetSearch ? statusLabel : `${statusLabel} (${exec.name})`;

                        const args = parseArguments(exec.arguments);
                        const apiCalls = exec.apiCalls ?? [];
                        // Expandable as soon as there is something to show: an
                        // error, the executed query, or the API calls the tool
                        // actually issued -- that last one being the only
                        // detail available for argument-less tools.
                        const canExpand = hasError || args !== null || apiCalls.length > 0;

                        return (
                            <div key={index} className={styles.toolCallContainer}>
                                <div
                                    className={styles.toolCallHeader}
                                    onClick={() => canExpand && toggleExpand(index)}
                                    style={{ cursor: canExpand ? 'pointer' : 'default' }}
                                >
                                    <div className={styles.toolCallStatus}>
                                        {exec.status === 'pending' && (
                                            <Icon name="fa fa-spinner" className={styles.toolCallSpinner} />
                                        )}
                                        {exec.status === 'success' && (
                                            <span className={styles.toolCallSuccess}>✓</span>
                                        )}
                                        {exec.status === 'error' && (
                                            <span className={styles.toolCallError}>✗</span>
                                        )}
                                    </div>
                                    <Icon name={iconName} size="sm" className={styles.toolCallIcon} />
                                    <span className={styles.toolCallName}>{displayStatus}</span>
                                    {exec.external && (
                                        <span className={styles.externalBadge}>Public internet</span>
                                    )}
                                    {canExpand && (
                                        <Icon name={isExpanded ? 'angle-down' : 'angle-right'} size="sm" />
                                    )}
                                </div>
                                {isExpanded && (args || apiCalls.length > 0) && (
                                    <div className={styles.toolCallArguments}>
                                        {args?.query && <code className={styles.toolCallQuery}>{args.query}</code>}
                                        {args?.rest && <span>{args.rest}</span>}
                                        {args?.raw && <span>{args.raw}</span>}
                                        {apiCalls.map((call, i) => (
                                            <code key={i} className={styles.toolCallApi}>{call}</code>
                                        ))}
                                    </div>
                                )}
                                {hasError && isExpanded && exec.error && (
                                    <div className={styles.toolCallErrorDetails}>
                                        {exec.error}
                                    </div>
                                )}
                            </div>
                        );
                    })}
                </div>
            )}
        </div>
    );
};

const getStyles = (theme: GrafanaTheme2) => ({
    containerWrapper: css`
    display: flex;
    flex-direction: column;
    gap: 8px;
    margin-bottom: 12px;
    width: fit-content;
    max-width: 100%;
  `,
    summaryHeader: css`
    display: inline-flex;
    width: fit-content;
    max-width: 100%;
    align-items: center;
    gap: 8px;
    padding: 8px 12px;
    font-size: 13px;
    color: ${theme.colors.text.primary};
    background: ${theme.colors.background.secondary};
    border: 1px solid ${theme.colors.border.weak};
    border-radius: 8px;
    cursor: pointer;
    user-select: none;

    &:hover {
      background: ${theme.colors.background.primary};
    }
  `,
    summaryText: css`
    font-family: ${theme.typography.fontFamily};
    font-weight: 500;
    white-space: nowrap;
  `,
    toolCallsWrapper: css`
    display: flex;
    flex-direction: column;
    gap: 8px;
    width: 100%;
    margin-left: 12px;
    padding-left: 12px;
    border-left: 2px solid ${theme.colors.border.weak};
  `,
    toolCallContainer: css`
    border: 1px solid ${theme.colors.border.weak};
    border-radius: 8px;
    background: ${theme.colors.background.primary};
    overflow: hidden;
  `,
    toolCallHeader: css`
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 8px 12px;
    font-size: 13px;
    color: ${theme.colors.text.primary};
    background: ${theme.colors.background.primary};

    &:hover {
      background: ${theme.colors.background.secondary};
    }
  `,
    toolCallStatus: css`
    display: flex;
    align-items: center;
    justify-content: center;
    width: 16px;
    height: 16px;
  `,
    toolCallSpinner: css`
    color: ${brand.red};
    font-size: 14px;
    animation: spin 1s linear infinite;
    @keyframes spin {
      0% { transform: rotate(0deg); }
      100% { transform: rotate(360deg); }
    }
  `,
    toolCallSuccess: css`
    color: ${theme.colors.success.text};
    font-weight: bold;
    font-size: 14px;
  `,
    toolCallError: css`
    color: ${theme.colors.error.text};
    font-weight: bold;
    font-size: 14px;
  `,
    toolCallName: css`
    font-family: ${theme.typography.fontFamilyMonospace};
    flex: 1;
  `,
    toolCallIcon: css`
    color: ${theme.colors.text.secondary};
  `,
    externalBadge: css`
    font-family: ${theme.typography.fontFamily};
    font-size: 10px;
    font-weight: 500;
    text-transform: uppercase;
    letter-spacing: 0.02em;
    color: ${theme.colors.warning.text};
    background: ${theme.colors.warning.transparent};
    border: 1px solid ${theme.colors.warning.border};
    border-radius: 4px;
    padding: 2px 6px;
    white-space: nowrap;
  `,
    toolCallErrorDetails: css`
    padding: 8px 12px;
    border-top: 1px solid ${theme.colors.border.weak};
    background: ${theme.colors.background.secondary};
    color: ${theme.colors.error.text};
    font-size: 12px;
    font-family: ${theme.typography.fontFamilyMonospace};
    white-space: pre-wrap;
    word-break: break-word;
  `,
    toolCallArguments: css`
    padding: 8px 12px;
    border-top: 1px solid ${theme.colors.border.weak};
    background: ${theme.colors.background.secondary};
    color: ${theme.colors.text.secondary};
    font-size: 12px;
    font-family: ${theme.typography.fontFamilyMonospace};
    white-space: pre-wrap;
    word-break: break-word;
  `,
    // The query is what the reader came to check: set apart from the rest of
    // the arguments, which are just call context.
    // What the tool actually asked Grafana for: the only information
    // available for list_datasources, list_folders and the like.
    toolCallApi: css`
    display: block;
    margin-top: 4px;
    color: ${theme.colors.text.secondary};
  `,
    toolCallQuery: css`
    display: block;
    margin-bottom: 6px;
    padding: 6px 8px;
    border-left: 2px solid ${theme.colors.primary.border};
    background: ${theme.colors.background.primary};
    color: ${theme.colors.text.primary};
  `,
});
