import { GrafanaTheme2 } from '@grafana/data';
import { css } from '@emotion/css';
import { brand } from '../../../brand';

export
const getStyles = (theme: GrafanaTheme2) => ({
  // Grafana's own <Modal> renders its close button (an internal IconButton)
  // through a Portal, so it's a real DOM descendant of the className passed
  // here even though it's outside this component's own React tree -- lets a
  // plain descendant selector reach it. Grafana's default :focus-visible
  // ring reads as too heavy/opaque against this modal's dark background;
  // this only softens it for this specific modal, not Grafana-wide.
  historyModal: css`
background: rgba(17, 18, 23, 0.88) !important;
backdrop-filter: blur(8px);
& *:focus-visible {
  outline: none !important;
  box-shadow: 0 0 0 2px rgba(43, 106, 219, 0.2) !important;
}
`,
  container: css`
display: flex;
flex-direction: column;
height: 100%;
// Real user-reported finding, 2026-08-06: reduced opacity alone (tried
// background.primary at 92%/70%, background.secondary at 60%/45%, plus
// backdrop-filter blur/brightness) was confirmed live every time
// (getComputedStyle) to genuinely apply, but stayed invisible once a real
// conversation filled the view -- a pure luminance shift between two
// near-black grays is too subtle for a screen/screenshot to show, and gets
// even harder to spot once message bubbles/accordions cover most of the
// visible area, leaving only thin slivers of the actual background exposed.
// Reusing landingContainer's own colored radial-gradient glow (already
// confirmed visible there live) instead of another luminance-only tweak --
// real color variation reads as translucent regardless of screen/viewing
// conditions, unlike near-black-vs-near-black. Stronger than
// landingContainer's own values and paired with a much more transparent
// base (35%, not 45-92%) so it stays visible under a full message list too.
background:
  radial-gradient(circle at top left, rgba(184, 32, 25, 0.22), transparent 45%),
  radial-gradient(circle at bottom right, rgba(5, 11, 47, 0.18), transparent 50%),
  color-mix(in srgb, ${theme.colors.background.secondary} 35%, transparent);
font-family: ${theme.typography.fontFamily};
position: relative;
user-select: none;
cursor: default;
`,
  landingContainer: css`
display: flex;
flex-direction: column;
align-items: center;
justify-content: center;
padding: ${theme.spacing(1)} ${theme.spacing(2)};
height: 100%;
max-height: 100%;
overflow: hidden;
position: relative;
background:
  radial-gradient(circle at top left, rgba(184, 32, 25, 0.14), transparent 34%),
  radial-gradient(circle at bottom right, rgba(5, 11, 47, 0.06), transparent 34%),
  linear-gradient(180deg, rgba(5, 11, 47, 0.08), transparent 42%);
`,
  historyButtonDiscreet: css`
position: absolute;
top: ${theme.spacing(1)};
right: ${theme.spacing(5)};
padding: ${theme.spacing(0.5)};
color: ${theme.colors.text.secondary};
opacity: 0.6;
background: transparent;
border: none;
border-radius: ${theme.shape.radius.default};
cursor: pointer;
display: flex;
align-items: center;
justify-content: center;

&:hover {
  opacity: 1;
}
`,
  agentInlineButton: css`
cursor: pointer;
color: ${theme.colors.text.secondary};
opacity: 0.8;
width: 32px;
height: 32px;
border-radius: 50%;
border: 1px solid ${theme.colors.border.weak};
background: transparent;
display: flex;
align-items: center;
justify-content: center;
transition: all 0.2s;

&:hover {
  opacity: 1;
  border-color: ${brand.navy};
  color: ${theme.isDark ? brand.surface : brand.navy};
  transform: scale(1.05);
}

&:focus {
  outline: none;
}

&:focus-visible {
  box-shadow: 0 0 0 2px rgba(43, 106, 219, 0.25);
}
`,
  // Color comes from the per-agent palette (--agent-color / --agent-color-bg,
  // set inline in ChatInterface.tsx from AGENT_TAG_COLORS) -- deliberately
  // NEVER blue, so it never reads as "the Enviar button changed color" when
  // sitting right next to it. Same subtle glow-pulse treatment for all agents.
  agentInlineButtonActive: css`
opacity: 1;
border-color: var(--agent-color, ${brand.navy});
color: var(--agent-color, ${brand.navy});
background: var(--agent-color-bg, ${theme.isDark ? 'rgba(255, 255, 255, 0.12)' : 'rgba(0, 0, 0, 0.06)'});
animation: agentGlowPulse 3s ease-in-out infinite;

@keyframes agentGlowPulse {
  0%, 100% { box-shadow: 0 0 0 0 transparent; }
  50% { box-shadow: 0 0 5px 2px var(--agent-color-bg, transparent); }
}
`,
  historyButtonDiscreetInline: css`
padding: ${theme.spacing(0.5)};
color: ${theme.colors.text.secondary};
opacity: 0.6;
background: transparent;
border: none;
border-radius: ${theme.shape.radius.default};
cursor: pointer;
display: flex;
align-items: center;
justify-content: center;

&:hover {
  opacity: 1;
}
`,
  settingsButton: css`
position: absolute;
top: ${theme.spacing(1)};
right: ${theme.spacing(1)};
padding: ${theme.spacing(0.5)};
color: ${theme.colors.text.secondary};
background: transparent;
border: none;
border-radius: ${theme.shape.radius.default};
cursor: pointer;
display: flex;
align-items: center;
justify-content: center;
line-height: 1;
&:hover {
  color: ${theme.colors.text.primary};
  background: ${theme.colors.action.hover};
}
`,
  landingContent: css`
max-width: 960px;
width: 100%;
display: flex;
flex-direction: column;
align-items: center;
justify-content: center;
height: 100%;
max-height: 100%;
overflow: hidden;
user-select: none;
cursor: default;
`,
  header: css`
display: flex;
align-items: center;
gap: ${theme.spacing(2)};
`,
  logo: css`
position: relative;
display: flex;
justify-content: center;
align-items: center;
flex-shrink: 0;
margin-top: -${theme.spacing(4)};
margin-bottom: ${parseFloat(theme.spacing(1)) + 19}px;
`,
  logoImage: css`
width: 280px;
height: 280px;
object-fit: contain;
`,
  // A single light-sweep pass across the logo, masked to its own silhouette
  // (mask-image: the same PNG) so the shine only crosses the wolf's shape,
  // not a rectangular box around it. Plays once (animation-iteration-count:
  // 1) the moment the backend is confirmed reachable -- never loops.
  logoShine: css`
position: absolute;
inset: 0;
width: 280px;
height: 280px;
pointer-events: none;
mask-size: contain;
mask-repeat: no-repeat;
mask-position: center;
-webkit-mask-size: contain;
-webkit-mask-repeat: no-repeat;
-webkit-mask-position: center;
background: linear-gradient(
  100deg,
  transparent 30%,
  rgba(255, 255, 255, 0.08) 47%,
  rgba(255, 255, 255, 0.08) 53%,
  transparent 70%
);
background-size: 300% 100%;
background-position: 150% 0;
animation: logoShineSweep 1.4s ease-in-out 1;

@keyframes logoShineSweep {
  from { background-position: 150% 0; }
  to { background-position: -50% 0; }
}
`,
  title: css`
font-size: 24px;
font-weight: 700;
line-height: 1.2;
margin: -2px 0 10px 0;
color: ${theme.isDark ? brand.surface : brand.navy};
`,
  subtitle: css`
font-size: 15px;
color: ${theme.colors.text.secondary};
margin: ${theme.spacing(0.5)} 0 ${theme.spacing(3)} 0;
text-align: center;
`,
  landingInputWrapper: css`
    position: relative;
width: 100%;
max-width: 800px;
flex-shrink: 0;
background: ${theme.colors.background.secondary};
border-radius: 16px;
padding: ${theme.spacing(1)};
border: 1px solid transparent;
background-image: linear-gradient(${theme.colors.background.secondary}, ${theme.colors.background.secondary}), linear-gradient(90deg, ${brand.red}, ${brand.navy});
background-origin: border-box;
background-clip: padding-box, border-box;
display: flex;
flex-direction: column;
gap: ${theme.spacing(0.75)};
text-align: left;
box-shadow: 0 18px 44px rgba(5, 11, 47, 0.18);
`,
  footerIconBadge: css`
width: 32px;
height: 32px;
display: flex;
align-items: center;
justify-content: center;
flex-shrink: 0;
color: ${theme.colors.text.secondary};
opacity: 0.75;
`,
  landingTextArea: css`
background: transparent;
border: none;
resize: none;
font-size: 16px;
min-height: 32px;
    &:focus {
  outline: none;
  box-shadow: none;
}
`,
  placeholderItalic: css`
    &::placeholder {
      font-style: italic;
    }
`,
  landingInputFooter: css`
display: flex;
justify-content: space-between;
align-items: center;
`,
  landingActions: css`
display: flex;
align-items: center;
gap: ${theme.spacing(1)};
`,
  landingSendButton: css`
background: ${brand.sendBlue};
border: none;
border-radius: 50%;
width: 32px;
height: 32px;
padding: 0;
display: flex;
align-items: center;
justify-content: center;
cursor: pointer;
transition: all 0.2s;
    
    &:hover {
  background: ${brand.red};
  transform: scale(1.05);
}
    
    &:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}
`,
  footerLinks: css`
display: flex;
justify-content: center;
width: 100%;
max-width: 760px;
flex-shrink: 0;
margin-top: ${theme.spacing(5)};
gap: ${theme.spacing(2)};
`,
  footerLink: css`
position: relative;
display: flex;
align-items: center;
flex: 1;
min-width: 0;
gap: ${theme.spacing(1)};
text-align: left;
cursor: pointer;
padding: ${theme.spacing(0.75)};
border: 1px solid ${brand.border};
border-radius: 8px;
background: ${theme.colors.background.secondary};
    &:hover {
  opacity: 0.8;
  border-color: ${brand.redSoft};
}
`,
  footerLinkEditing: css`
flex-direction: column;
align-items: stretch;
cursor: default;
gap: ${theme.spacing(0.5)};
`,
  quickPromptTextArea: css`
min-width: 0;
overflow: hidden;
`,
  quickPromptEditIcon: css`
position: absolute;
top: ${theme.spacing(1)};
right: ${theme.spacing(1)};
opacity: 0.18;
color: ${theme.colors.text.secondary};
transition: opacity 0.2s;
cursor: pointer;
    &:hover {
  opacity: 1;
  color: ${theme.colors.text.primary};
}
`,
  quickPromptEditTitle: css`
background: ${theme.colors.background.primary};
border: 1px solid ${theme.colors.border.weak};
border-radius: 4px;
padding: 4px 6px;
font-size: 14px;
font-weight: bold;
color: ${theme.colors.text.primary};
width: 100%;
`,
  quickPromptEditContent: css`
background: ${theme.colors.background.primary};
border: 1px solid ${theme.colors.border.weak};
border-radius: 4px;
padding: 4px 6px;
font-size: 12px;
color: ${theme.colors.text.primary};
width: 100%;
resize: vertical;
`,
  quickPromptEditActions: css`
display: flex;
justify-content: flex-end;
gap: ${theme.spacing(0.5)};
`,
  linkTitle: css`
font-weight: bold;
font-size: 14px;
`,
  linkDesc: css`
font-size: 12px;
color: ${theme.colors.text.secondary};
white-space: nowrap;
overflow: hidden;
text-overflow: ellipsis;
`,
  conversationTimestamp: css`
text-align: center;
font-size: 12px;
color: ${theme.colors.text.secondary};
opacity: 0.7;
margin-bottom: ${theme.spacing(1)};
`,
  messageList: css`
flex-grow: 1;
min-height: 0;
overflow-y: auto;
padding: ${theme.spacing(2)};
// Extra top padding equal to chatHeader's own height (44px, measured) --
// chatHeader is now position:absolute, overlapping the top of this
// scrollable area (same technique as inputArea's bottom overlap) so its
// translucent backdrop-filter actually has real message content behind
// it to blur, instead of the header sitting entirely above where any
// scrolled content is ever rendered.
padding-top: 60px;
display: flex;
flex-direction: column;
gap: ${theme.spacing(2)};
scrollbar-width: thin;
scrollbar-color: ${theme.colors.border.medium} transparent;

&::-webkit-scrollbar {
  width: 6px;
}
&::-webkit-scrollbar-track {
  background: transparent;
}
&::-webkit-scrollbar-thumb {
  background: ${theme.colors.border.medium};
  border-radius: 4px;
}
`,
  panelPreviewMessageList: css`
background:
  radial-gradient(circle at top left, rgba(184, 32, 25, 0.24), transparent 42%),
  radial-gradient(circle at bottom right, rgba(5, 11, 47, 0.2), transparent 48%),
  linear-gradient(180deg, rgba(184, 32, 25, 0.08), transparent 38%);
padding-top: ${theme.spacing(2)};
`,
  // Wraps the colored bubble + the action row (edit/copy) below it as two
  // stacked, independently-styled elements -- the actions row used to live
  // INSIDE the bubble div, sharing its colored background/rounded corners,
  // which is why it always read as "glued to the bubble" no matter the
  // margin. Splitting them lets the actions sit on the plain message-list
  // background, clearly below and outside the bubble.
  messageWrapper: css`
display: flex;
flex-direction: column;
max-width: 80%;
min-width: 0;
gap: ${theme.spacing(0.5)};
`,
  userMessageWrapper: css`
align-self: flex-end;
`,
  assistantMessageWrapper: css`
align-self: flex-start;
`,
  message: css`
padding: ${theme.spacing(1.5)};
border-radius: 12px;
font-size: ${theme.typography.body.fontSize};
transition: all 0.2s ease-out;
animation: fadeInScale 0.3s ease-out;
max-width: 100%;
min-width: 0;
overflow: hidden;

@keyframes fadeInScale {
      from {
    opacity: 0;
    transform: scale(0.95);
  }
      to {
    opacity: 1;
    transform: scale(1);
  }
}
`,
  userMessage: css`
background: linear-gradient(135deg, ${brand.red}, ${brand.redSoft});
color: ${brand.surface};
font-weight: 500;
border-bottom-right-radius: 2px;
`,
  // Panel-preview mode: a duller, more opaque/muted red -- deliberately
  // reads as "not a live chat bubble you can reply to", distinct from the
  // vivid gradient used in a normal conversation.
  userMessagePreview: css`
background: rgba(120, 40, 40, 0.92);
color: ${brand.surface};
font-weight: 500;
border-bottom-right-radius: 2px;
`,
  assistantMessage: css`
background: ${theme.colors.background.secondary};
color: ${theme.colors.text.primary};
border-bottom-left-radius: 2px;
border: 1px solid ${brand.border};
`,
  messageContent: css`
white-space: pre-wrap;
word-wrap: break-word;
overflow-wrap: anywhere;
min-width: 0;
max-width: 100%;
transition: opacity 0.15s ease-in-out;
user-select: text;
cursor: text;

    /* Markdown Styles */
    p {
  margin: 0 0 ${theme.spacing(1)} 0;
      &:last-child {
    margin-bottom: 0;
  }
}

h1, h2, h3, h4, h5, h6 {
  margin-top: ${theme.spacing(2)};
  margin-bottom: ${theme.spacing(2)};
  font-weight: 600;
  color: ${theme.colors.text.primary};
}
    
    h1 { font-size: 1.5em; }
    h2 { font-size: 1.3em; }
    h3 { font-size: 1.1em; }

ul, ol {
  margin: 0;
  padding-left: ${theme.spacing(3)};
}
    
    li {
  margin: 0;
  line-height: 1.5;
}
    
    code {
  background: ${theme.colors.background.primary};
  padding: 2px 4px;
  border-radius: 3px;
  font-family: ${theme.typography.fontFamilyMonospace};
  font-size: 0.9em;
  white-space: pre-wrap;
  overflow-wrap: anywhere;
  word-break: break-word;
}
    
    pre {
  margin: ${theme.spacing(1, 0)};
  border-radius: 4px;
  max-width: 100%;
  overflow-x: auto;
      
      code {
    background: transparent;
    padding: 0;
    border-radius: 0;
  }
}
    
    blockquote {
  border-left: 4px solid ${theme.colors.border.strong};
  margin: ${theme.spacing(1, 0)};
  padding-left: ${theme.spacing(2)};
  color: ${theme.colors.text.secondary};
  font-style: italic;
}
    
    table {
  border-collapse: collapse;
  width: 100%;
  margin: ${theme.spacing(1, 0)};
  font-size: 0.9em;
}

th, td {
  border: 1px solid ${theme.colors.border.weak};
  padding: ${theme.spacing(1)};
  text-align: left;
}
    
    th {
  background: ${theme.colors.background.primary};
  font-weight: 600;
}
    
    a {
  color: ${theme.colors.primary.text};
  text-decoration: none;
      &:hover {
    text-decoration: underline;
  }
}
    
    img {
  max-width: 100%;
  border-radius: 4px;
}
`,
  scrollButton: css`
position: absolute;
bottom: 120px;
right: ${theme.spacing(1)};
width: 36px;
height: 36px;
background: none;
border: none;
box-shadow: none;
display: flex;
align-items: center;
justify-content: center;
cursor: pointer;
z-index: 10;
color: ${theme.colors.text.secondary};
opacity: 0.8;
transition: opacity 0.2s;
    &:hover {
  opacity: 1;
  color: ${theme.colors.text.primary};
}
`,
  inputArea: css`
padding: 8px ${theme.spacing(1.5)} ${theme.spacing(1.5)};
border-top: 1px solid ${theme.colors.border.weak};
display: flex;
flex-direction: column;
gap: ${theme.spacing(0.5)};
// Floats over the message list (a sibling still occupying normal flow, so
// this needs its own stacking) instead of pushing it up -- lets whatever
// message scrolled to the bottom show faintly through the translucent
// backdrop instead of being hard-cut by an opaque panel. Same
// rgba+backdrop-filter technique already used for historyModal's backdrop,
// just lighter (that one is a near-opaque 0.88 over a full-screen dialog;
// this one is deliberately more see-through since content behind it is the
// whole point). The textarea/buttons below stay fully opaque on their own
// -- only this wrapper's own background is translucent.
position: absolute;
left: 0;
right: 0;
bottom: 0;
z-index: 5;
background: rgba(17, 18, 23, 0.92);
backdrop-filter: blur(10px);
-webkit-backdrop-filter: blur(10px);
`,
  thinkingBlockWrapper: css`
margin-bottom: ${theme.spacing(1)};
border: 1px solid ${theme.colors.border.weak};
border-radius: 6px;
background: ${theme.colors.background.primary};
overflow: hidden;
`,
  thinkingHeader: css`
display: flex;
align-items: center;
gap: ${theme.spacing(1)};
padding: ${theme.spacing(1)};
cursor: pointer;
user-select: none;
background: ${theme.colors.background.secondary};
    &:hover {
  background: ${theme.colors.action.hover};
}
`,
  thinkingLabel: css`
color: ${theme.colors.text.secondary};
font-size: ${theme.typography.bodySmall.fontSize};
`,

  thinkingContent: css`
padding: ${theme.spacing(1.5)};
border-top: 1px solid ${theme.colors.border.weak};
font-size: ${theme.typography.bodySmall.fontSize};
color: ${theme.colors.text.secondary};
background: ${theme.colors.background.primary};
`,
  inputWrapper: css`
position: relative;
display: flex;
flex-direction: column;
align-items: stretch;
background: ${theme.colors.background.secondary};
border-radius: 16px;
padding: ${theme.spacing(2)};
border: 1px solid transparent;
background-image: linear-gradient(${theme.colors.background.secondary}, ${theme.colors.background.secondary}), linear-gradient(90deg, ${brand.red}, ${brand.navy});
background-origin: border-box;
background-clip: padding-box, border-box;
    
    &:focus-within {
  outline: none;
  box-shadow: none;
}

textarea:focus {
  outline: none;
  box-shadow: none;
  border: none;
}
`,
  inputWrapperLoading: css`
position: relative;
    
    &::before {
  content: '';
  position: absolute;
  inset: -3px;
  border-radius: 19px;
  padding: 3px;
  background: linear-gradient(90deg,
    ${brand.red},
    ${brand.redSoft},
    ${brand.navy},
    ${brand.red}
  );
  background-size: 200% 100%;
  -webkit-mask: linear-gradient(#fff 0 0) content-box, linear-gradient(#fff 0 0);
  -webkit-mask-composite: xor;
  mask-composite: exclude;
  animation: rotateGradient 3s linear infinite;
  pointer-events: none;
  filter: blur(1px);
  opacity: 0.8;
}

@keyframes rotateGradient {
  0% {
    background-position: 0% 50%;
}
100% {
  background-position: 200% 50%;
      }
    }
`,
  textArea: css`
resize: none;
padding-right: ${theme.spacing(1.5)};
padding-left: ${theme.spacing(1.5)};
border-radius: 12px;
display: flex;
align-items: center;
padding-top: ${theme.spacing(1.5)};
padding-bottom: ${theme.spacing(1.5)};
background: transparent;
border: none;
width: 100%;
scrollbar-width: thin;
scrollbar-color: ${theme.colors.border.medium} transparent;

    &:focus {
  outline: none;
}

&::-webkit-scrollbar {
  width: 6px;
}
&::-webkit-scrollbar-track {
  background: transparent;
}
&::-webkit-scrollbar-thumb {
  background: ${theme.colors.border.medium};
  border-radius: 4px;
}
`,
  inputFooter: css`
display: flex;
justify-content: flex-end;
align-items: center;
padding-top: ${theme.spacing(0.5)};
margin-top: ${theme.spacing(0.5)};
`,
  inputActions: css`
display: flex;
gap: ${theme.spacing(1.5)};
align-items: center;
`,
  iconButton: css`
background: transparent;
border: 1px solid ${theme.colors.border.weak};
border-radius: 50%;
padding: 0;
font: inherit;
cursor: pointer;
color: ${theme.colors.text.secondary};
opacity: 0.9;
transition: all 0.2s;
display: flex;
align-items: center;
justify-content: center;
width: 32px;
height: 32px;
flex-shrink: 0;

    &:hover {
  opacity: 1;
  border-color: ${brand.navy};
  color: ${theme.isDark ? brand.surface : brand.navy};
  transform: scale(1.05);
}

    &.active {
  color: ${theme.colors.error.main};
  border-color: ${theme.colors.error.main};
  opacity: 1;
  animation: pulse 1.5s infinite;
}

    &:focus {
  outline: none;
}

    &:focus-visible {
  box-shadow: 0 0 0 2px rgba(43, 106, 219, 0.25);
}

@keyframes pulse {
  0% { transform: scale(1); }
  50% { transform: scale(1.1); }
  100% { transform: scale(1); }
}
`,
  sendIconButton: css`
border: none;
padding: 0;
font: inherit;
cursor: pointer;
background: ${brand.sendBlue};
border-radius: 50%;
width: 32px;
height: 32px;
display: flex;
align-items: center;
justify-content: center;
transition: all 0.2s;

    &:hover {
  background: ${brand.red};
  transform: scale(1.1);
}

    &:focus {
  outline: none;
}

    &:focus-visible {
  box-shadow: 0 0 0 2px rgba(43, 106, 219, 0.25);
}
`,
  chatHeader: css`
display: flex;
justify-content: space-between;
align-items: center;
padding: ${theme.spacing(0.75)} ${theme.spacing(1)};
// Real, reproduced bug: position:sticky here had nothing to actually
// stick to or overlap -- chatHeader's own parent doesn't scroll (only the
// nested messageList does), so this header sat in its own dedicated
// space entirely ABOVE where any message content is ever painted.
// backdrop-filter had no real pixels behind it to blur, so the
// translucent background just showed flat against the parent's own
// (near-identical) dark color -- confirmed visually opaque in two
// separate real browsers, not a caching artifact. Switched to
// position:absolute overlapping the top of messageList (same technique
// as inputArea's bottom overlap) -- messageList.padding-top is sized to
// match, so scrolled messages now genuinely render behind this header.
position: absolute;
top: 0;
left: 0;
right: 0;
z-index: 10;
background: rgba(17, 18, 23, 0.92);
backdrop-filter: blur(10px);
-webkit-backdrop-filter: blur(10px);
`,
  headerLeft: css`
display: flex;
align-items: center;
gap: ${theme.spacing(1)};
z-index: 1;
`,
  thinkingIndicator: css`
display: flex;
align-items: center;
gap: ${theme.spacing(1)};
margin-top: ${theme.spacing(0.5)};
color: ${theme.colors.text.secondary};
font-size: 12px;
font-weight: ${theme.typography.fontWeightMedium};
`,
  thinkingStatusText: css`
line-height: 1;
color: ${theme.colors.text.disabled};
`,
  thinkingDots: css`
display: flex;
align-items: center;
gap: 7px;
`,
  thinkingDot: css`
width: 11px;
height: 11px;
border-radius: 50%;
background: linear-gradient(135deg, ${brand.red}, ${brand.redSoft});
animation: dotBreathe 1.2s ease-in-out infinite;

@keyframes dotBreathe {
  0%, 80%, 100% { opacity: 0.55; transform: scale(0.85); }
  40% { opacity: 1; transform: scale(1); }
}
`,
  codeBlockWrapper: css`
margin: 8px 0;
border-radius: 4px;
overflow: hidden;
background: ${theme.colors.background.primary};
`,
  codeBlockHeader: css`
display: flex;
align-items: center;
justify-content: space-between;
padding: 8px 12px;
background: ${theme.colors.background.primary};
border-bottom: 1px solid ${theme.colors.border.weak};
`,
  languageLabel: css`
font-size: 12px;
color: ${theme.colors.text.secondary};
font-family: ${theme.typography.fontFamilyMonospace};
text-transform: lowercase;
`,
  copyButton: css`
display: flex;
align-items: center;
gap: 4px;
background: transparent;
border: none;
color: ${theme.colors.text.secondary};
cursor: pointer;
font-size: 12px;
padding: 4px 8px;
border-radius: 4px;
transition: all 0.2s;
    
    &:hover {
  background: ${theme.colors.background.secondary};
  color: ${theme.colors.text.primary};
}
    
    svg {
  width: 14px;
  height: 14px;
}
`,
  editedLabel: css`
font-size: 11px;
font-style: italic;
color: ${theme.colors.text.secondary};
`,
  editingMessageBanner: css`
display: flex;
align-items: center;
justify-content: space-between;
gap: ${theme.spacing(1)};
margin: 0 0 ${theme.spacing(0.75)};
padding: 0 ${theme.spacing(0.25)};
border: none;
background: transparent;
color: ${theme.colors.text.secondary};
font-size: 12px;
`,
  editingMessageInfo: css`
display: inline-flex;
align-items: center;
gap: ${theme.spacing(0.75)};
min-width: 0;
font-weight: 500;
color: ${theme.colors.text.secondary};
`,
  editingMessageCancel: css`
border: none;
background: transparent;
color: ${theme.colors.text.secondary};
cursor: pointer;
font-size: 12px;
font-weight: 500;
padding: 0;
border-radius: 4px;
opacity: 0.9;

&:hover {
  color: ${theme.colors.text.primary};
  opacity: 1;
}
`,
  // Same look as ActivityAccordion's pill/header (the "✓ Activity · N steps"
  // already used for tool calls/thinking) -- reuses that pattern instead of
  // an underlined blue link, which clashed with the rest of the UI.
  // align-self: flex-end because the parent (messageWrapper) is a flex
  // column max-width:80%; without it the pill would inherit the message
  // block's width instead of staying compact (width: fit-content) aligned
  // right like the rest of the user message's own actions.
  replacedBranchToggle: css`
display: inline-flex;
align-self: flex-end;
align-items: center;
gap: ${theme.spacing(1)};
width: fit-content;
max-width: 100%;
padding: ${theme.spacing(0.5)} ${theme.spacing(1)};
border: 1px solid ${theme.colors.border.weak};
border-radius: 6px;
background: ${theme.colors.background.secondary};
color: ${theme.colors.text.secondary};
font-size: 11px;
cursor: pointer;

&:hover {
  background: ${theme.colors.action.hover};
}
`,
  replacedBranchPanel: css`
align-self: flex-end;
margin-top: ${theme.spacing(0.5)};
padding-left: ${theme.spacing(1)};
border-left: 2px solid ${theme.colors.border.weak};
font-size: 12px;
color: ${theme.colors.text.secondary};
max-width: 100%;
`,
  replacedBranchEntry: css`
margin-bottom: ${theme.spacing(0.5)};
white-space: pre-wrap;
word-break: break-word;

&:last-child {
  margin-bottom: 0;
}
`,
  messageActions: css`
display: flex;
align-items: center;
justify-content: flex-end;
gap: 2px;

opacity: 0.6;
transition: opacity 0.2s;
    
    &:hover {
  opacity: 1;
}
`,
  messageActionButton: css`
background: transparent;
border: none;
color: ${theme.colors.text.secondary};
cursor: pointer;
padding: 2px;
border-radius: 4px;
display: flex;
align-items: center;
justify-content: center;
transition: all 0.2s;

    &:hover {
  background: ${theme.colors.background.secondary};
  color: ${theme.colors.text.primary};
}
`,
  filePreviewList: css`
display: flex;
gap: ${theme.spacing(1)};
padding: ${theme.spacing(1)};
background: ${theme.colors.background.secondary};
border-radius: 8px;
margin-bottom: ${theme.spacing(1)};
overflow-x: auto;
max-width: 100%;
width: fit-content;
`,
  filePreviewItem: css`
position: relative;
flex-shrink: 0;
background: ${theme.colors.background.primary};
border-radius: 4px;
padding: 4px;
border: 1px solid ${theme.colors.border.weak};
`,
  previewImage: css`
max-height: 60px;
border-radius: 4px;
display: block;
`,
  previewContainer: css`
display: flex;
flex-direction: column;
gap: 4px;
width: 120px;
`,
  textPreviewContent: css`
font-family: ${theme.typography.fontFamilyMonospace};
font-size: 9px;
background: ${theme.colors.background.canvas};
padding: 4px;
border-radius: 4px;
height: 60px;
overflow: hidden;
white-space: pre-wrap;
border: 1px solid ${theme.colors.border.weak};
color: ${theme.colors.text.secondary};
`,
  fileName: css`
display: flex;
align-items: center;
gap: 4px;
font-size: 10px;
color: ${theme.colors.text.primary};
overflow: hidden;
text-overflow: ellipsis;
white-space: nowrap;
`,
  removeFileButton: css`
position: absolute;
top: -6px;
right: -6px;
background: ${theme.colors.background.primary};
border: 1px solid ${theme.colors.border.weak};
border-radius: 50%;
cursor: pointer;
color: ${theme.colors.text.secondary};
display: flex;
align-items: center;
justify-content: center;
width: 20px;
height: 20px;
padding: 0;
box-shadow: 0 2px 4px rgba(0, 0, 0, 0.1);
    
    &:hover {
  color: ${theme.colors.error.text};
  border-color: ${theme.colors.error.border};
}
    
    svg {
  width: 12px;
  height: 12px;
}
`,
  expandIconOverlay: css`
position: absolute;
top: 0;
left: 0;
width: 100%;
height: 100%;
background: rgba(0, 0, 0, 0.5);
display: flex;
align-items: center;
justify-content: center;
opacity: 0;
transition: opacity 0.2s ease;
cursor: pointer;
color: white;
    
    &:hover {
  opacity: 1;
}
`,
  headerRight: css`
display: flex;
align-items: center;
gap: ${theme.spacing(1)};
`,
  contextUsageBadge: css`
font-size: 11px;
color: ${theme.colors.text.secondary};
padding: 2px ${theme.spacing(1)};
border-radius: 12px;
border: 1px solid ${theme.colors.border.weak};
white-space: nowrap;
`,
  // Transient activity label (e.g. "Compacting conversation context...") -- separate
  // from the "..." thinking dots, which just mean "generating"; this means
  // "a specific background step is happening right now". Centered in the
  // conversation itself (not a tiny header badge crammed next to the token
  // count) so it reads as a real, noticeable event.
  activityStatusBanner: css`
display: flex;
justify-content: center;
margin: ${theme.spacing(1)} 0;
`,
  activityStatusBadge: css`
font-size: 12px;
font-style: italic;
color: ${theme.colors.text.secondary};
padding: 4px ${theme.spacing(1.5)};
border-radius: 12px;
border: 1px dashed ${theme.colors.border.weak};
white-space: nowrap;
animation: activityStatusFade 1.6s ease-in-out infinite;

@keyframes activityStatusFade {
  0%, 100% { opacity: 0.55; }
  50% { opacity: 1; }
}
`,
  toolCallsWrapper: css`
display: flex;
flex-direction: column;
gap: 8px;
margin-bottom: 12px;
width: 100%;
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
color: ${theme.colors.primary.text};
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
  disclaimer: css`
text-align: center;
font-size: 11px;
line-height: 1.2;
margin: 0;
padding: 0 0 ${theme.spacing(0.5)} 0;
color: ${theme.colors.text.secondary};
opacity: 0.7;
flex: none;
`,
  historyEmpty: css`
display: flex;
flex-direction: column;
align-items: center;
gap: ${theme.spacing(1.5)};
color: ${theme.colors.text.secondary};
font-size: 13px;
padding: ${theme.spacing(5)} 0;
text-align: center;
`,
  historyEmptyIcon: css`
opacity: 0.35;
`,
  historyList: css`
display: flex;
flex-direction: column;
gap: ${theme.spacing(1)};
max-height: 60vh;
overflow-y: auto;
padding-right: 2px;

&::-webkit-scrollbar {
  width: 6px;
}
&::-webkit-scrollbar-thumb {
  background: ${theme.colors.border.medium};
  border-radius: 4px;
}
`,
  historyItem: css`
display: flex;
align-items: center;
gap: ${theme.spacing(1.5)};
padding: ${theme.spacing(1.25)} ${theme.spacing(1.5)};
border-radius: 10px;
background: ${theme.colors.background.secondary};
border: 1px solid transparent;
transition: border-color 0.15s ease, transform 0.15s ease;

&:hover {
  border-color: ${brand.border};
  transform: translateX(2px);
}
`,
  historyItemIcon: css`
display: flex;
align-items: center;
justify-content: center;
width: 34px;
height: 34px;
border-radius: 9px;
flex-shrink: 0;
background: ${theme.isDark ? 'rgba(184, 32, 25, 0.14)' : 'rgba(184, 32, 25, 0.08)'};
color: ${brand.redSoft};
`,
  historyItemMain: css`
flex: 1;
min-width: 0;
cursor: pointer;
`,
  historyItemTitle: css`
font-size: 13px;
font-weight: 500;
white-space: nowrap;
overflow: hidden;
text-overflow: ellipsis;
`,
  // Colors are applied inline per agent (see AGENT_TAG_COLORS in the
  // component) -- this class only carries the shared shape/spacing.
  historyItemAgentTag: css`
display: inline-block;
margin-left: ${theme.spacing(1)};
padding: 1px 6px;
font-size: 10px;
font-weight: 600;
border-radius: 10px;
border: 1px solid transparent;
vertical-align: middle;
`,
  historyItemDate: css`
font-size: 11px;
color: ${theme.colors.text.secondary};
margin-top: 2px;
`,
  historyItemDelete: css`
cursor: pointer;
color: ${theme.colors.text.secondary};
flex-shrink: 0;
opacity: 0.6;
transition: opacity 0.15s, color 0.15s;

&:hover {
  opacity: 1;
  color: ${theme.colors.error.text};
}
`,
});
