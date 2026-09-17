import type { ChatSession } from '../types/chat.types';

import { downloadTextFile, exportFileName, sessionToMarkdown } from './chatExport';

const session = (overrides: Partial<ChatSession> = {}): ChatSession => ({
    id: 's1',
    title: 'Checkout latency',
    createdAt: Date.parse('2026-09-07T10:00:00Z'),
    updatedAt: Date.parse('2026-09-08T11:30:00Z'),
    messages: [
        { role: 'user', content: 'Why is checkout slow?' },
        {
            role: 'assistant',
            content: 'p99 latency is 2.3s, driven by the payment call.',
            toolExecutions: [
                {
                    name: 'query_prometheus',
                    status: 'success',
                    arguments: '{"query":"histogram_quantile(0.99, rate(x[5m]))"}',
                    apiCalls: ['GET /api/datasources/proxy/uid/prom/api/v1/query_range'],
                },
            ],
        },
    ],
    ...overrides,
});

describe('sessionToMarkdown', () => {
    it('writes the conversation as readable Markdown', () => {
        const md = sessionToMarkdown(session());

        expect(md).toContain('# Checkout latency');
        expect(md).toContain('### You');
        expect(md).toContain('Why is checkout slow?');
        expect(md).toContain('### Agent AI');
        expect(md).toContain('p99 latency is 2.3s');
    });

    // The tool calls are what makes an exported answer checkable months
    // later: "the error rate is 3%" without the query behind it is an
    // assertion, not evidence.
    it('keeps the queries behind an answer, folded away', () => {
        const md = sessionToMarkdown(session());

        expect(md).toContain('<details>');
        expect(md).toContain('1 tool call');
        expect(md).toContain('query_prometheus');
        expect(md).toContain('histogram_quantile');
        expect(md).toContain('GET /api/datasources/proxy/uid/prom/api/v1/query_range');
    });

    it('names attachments rather than embedding them', () => {
        const md = sessionToMarkdown(
            session({
                messages: [
                    {
                        role: 'user',
                        content: 'What does this show?',
                        attachments: [{ name: 'graph.png', content: 'AAAA...base64...', type: 'image' }],
                    },
                ],
            })
        );

        expect(md).toContain('graph.png');
        // A base64 payload would make the file unreadable and enormous.
        expect(md).not.toContain('AAAA...base64...');
    });

    it('leaves out raw tool results, which the answer already summarises', () => {
        const md = sessionToMarkdown(
            session({
                messages: [
                    { role: 'user', content: 'Any alerts?' },
                    { role: 'tool', content: '{"alerts":[...4000 characters of JSON...]}' },
                    { role: 'assistant', content: 'Two alerts are firing.' },
                ],
            })
        );

        expect(md).toContain('Two alerts are firing.');
        expect(md).not.toContain('4000 characters of JSON');
    });

    it('marks an edited or interrupted message as such', () => {
        const md = sessionToMarkdown(
            session({
                messages: [
                    { role: 'user', content: 'First question', edited: true },
                    { role: 'assistant', content: 'Partial ans', interrupted: true },
                ],
            })
        );

        expect(md).toMatch(/### You _\(edited\)_/);
        expect(md).toMatch(/### Agent AI _\(interrupted\)_/);
    });

    it('renders a notice as a quote, so it does not read as the assistant speaking', () => {
        const md = sessionToMarkdown(
            session({
                messages: [
                    {
                        role: 'assistant',
                        content: 'A Grafana admin must enable it.',
                        isUnavailableNotice: true,
                        noticeTitle: 'Agent AI is not enabled',
                    },
                ],
            })
        );

        expect(md).toContain('> **Agent AI is not enabled**');
        expect(md).toContain('> A Grafana admin must enable it.');
    });

    it('survives a conversation with nothing in it', () => {
        const md = sessionToMarkdown(session({ title: '', messages: [] }));
        expect(md).toContain('# Conversation');
    });
});

describe('exportFileName', () => {
    it('builds a name someone can find again', () => {
        expect(exportFileName(session(), 'md')).toBe('checkout-latency-2026-09-08.md');
    });

    it('reduces a title to something every filesystem accepts', () => {
        const name = exportFileName(session({ title: 'Grantm: sync/errors (p99!) — prod' }), 'md');
        expect(name).toMatch(/^[a-z0-9-]+-\d{4}-\d{2}-\d{2}\.md$/);
        expect(name).toContain('grantm-sync-errors');
    });

    it('falls back when the title carries nothing usable', () => {
        expect(exportFileName(session({ title: '???' }), 'json')).toMatch(/^conversation-\d{4}-\d{2}-\d{2}\.json$/);
    });

    it('truncates a very long title', () => {
        const name = exportFileName(session({ title: 'a'.repeat(200) }), 'md');
        expect(name.length).toBeLessThan(80);
    });
});

describe('downloadTextFile', () => {
    it('hands the browser a named file and cleans up after itself', () => {
        const createObjectURL = jest.fn(() => 'blob:fake');
        const revokeObjectURL = jest.fn();
        Object.defineProperty(URL, 'createObjectURL', { value: createObjectURL, writable: true });
        Object.defineProperty(URL, 'revokeObjectURL', { value: revokeObjectURL, writable: true });
        const click = jest.fn();
        let anchor: HTMLAnchorElement | undefined;
        let appended = false;
        const originalCreate = document.createElement.bind(document);
        jest.spyOn(document, 'createElement').mockImplementation((tag: string) => {
            const element = originalCreate(tag) as HTMLAnchorElement;
            if (tag === 'a') {
                element.click = click;
                anchor = element;
            }
            return element;
        });
        const appendChild = jest.spyOn(document.body, 'appendChild').mockImplementation((node: any) => {
            appended = true;
            return node;
        });

        jest.useFakeTimers();
        downloadTextFile('conversation.md', '# Hello');
        jest.runAllTimers();

        expect(click).toHaveBeenCalled();
        // Revoking in the same frame as the click cancels the download in
        // some browsers, so it has to happen on a later tick -- and it has
        // to happen at all, or the blob leaks for the life of the page.
        expect(revokeObjectURL).toHaveBeenCalledWith('blob:fake');
        // The anchor must never enter the document. Grafana's own
        // interceptLinkClicks handler listens on document, preventDefaults
        // any anchor with an href and no target, and navigates to it -- which
        // turned this download into "the blob URL opened as a page".
        expect(document.body.querySelector('a')).toBeNull();
        expect(appended).toBe(false);
        // Second guard: the interceptor skips anchors that declare a target.
        expect(anchor?.target).toBe('_self');
        expect(anchor?.download).toBe('conversation.md');

        jest.useRealTimers();
        appendChild.mockRestore();
        (document.createElement as jest.Mock).mockRestore();
    });
});
