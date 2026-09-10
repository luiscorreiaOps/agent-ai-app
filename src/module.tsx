import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import { AppPlugin, AppRootProps, PluginExtensionPoints, type PluginExtensionPanelContext } from '@grafana/data';
import { Alert, Icon, useStyles2 } from '@grafana/ui';
import { AppConfig } from './components/AppConfig';
import { AgentsPage } from './pages';
import { ChatInterface } from './components/features/ChatInterface/ChatInterface';
import { getStyles as getChatInterfaceStyles } from './components/features/ChatInterface/ChatInterface.styles';
import { PLUGIN_ID } from './constants';
import { fetchLimits, type Limits } from './api/client';
import { normalizeResponseLanguage } from './services/landingText';

const DEFAULT_LIMITS: Limits = {
  attachmentMaxBytes: 51200,
  enableStandaloneChat: true,
  enableDashboardIntegration: true,
  auditLogFullContent: false,
  responseLanguage: 'english',
  maintenanceMode: false,
  lightModeForDefaultAgent: false,
};

// Fetched once at module load, not per-render -- extension `configure`
// callbacks are synchronous (no async/await), so this cache is what lets
// addLink's configure() below decide "hide this" without blocking. By the
// time a user actually opens a panel menu or the command palette, this has
// long since resolved in practice. Defaults to "everything enabled" so a
// slow/failed fetch never hides a feature the admin actually wants. AppRoot
// below does NOT rely on this for its own rendering -- a plain module
// variable changing doesn't trigger a React re-render, so it keeps its own
// state (see useEffect there); this cache exists only for the synchronous
// configure() callbacks that have no other way to reach the real value.
let cachedLimits: Limits = DEFAULT_LIMITS;
fetchLimits()
  .then((limits) => { cachedLimits = limits; })
  .catch(() => {});

// See the DashboardPanelMenu addLink's onClick below for why this exists.
const PANEL_PREVIEW_REOPEN_COOLDOWN_MS = 2000;
let lastPanelPreviewOpenedAt = 0;

function DisabledNotice({ feature }: { feature: string }) {
  return (
    <div style={{ padding: '16px' }}>
      <Alert title={`${feature} is disabled`} severity="warning">
        This is turned off. It can be re-enabled from the plugin&apos;s Configuration page (Feature toggles).
      </Alert>
    </div>
  );
}

// Shown instead of the real standalone chat when Settings.MaintenanceMode is
// on (Configuration page's "Assistant Features" section) -- a planned-outage
// notice, distinct from DisabledNotice above: this is "temporarily down for a
// known reason", not "an admin turned this feature off". severity="info" is
// Grafana's blue alert style, matching the "blue warning" this was asked for.
// Only gates the standalone chat page -- the dashboard-panel-menu chat
// (addLink's onClick above) is unaffected.
//
// Deliberately reuses ChatInterface's own container/landingContainer/logo/
// settingsButton classes (imported from its styles module) instead of a
// bespoke layout -- this must look exactly like the normal empty-chat landing
// screen (same background gradient, same logo treatment, same gear icon),
// just with the greeting/quick-prompts/input/history button removed and an
// Alert in their place. A hand-rolled layout would drift from that background
// the moment ChatInterface's own styling changes.
function MaintenanceNotice() {
  const styles = useStyles2(getChatInterfaceStyles);
  // Same real-measured-height fix as ChatInterface's own standaloneHeight
  // (see its doc comment) -- a static 100vh/100dvh assumes this container
  // starts at viewport y=0, but standalone mode actually starts BELOW
  // Grafana's own top nav bar, whose height isn't a CSS constant either
  // plugin exposes. Confirmed live: min-height:100vh alone left the content
  // sitting high with dead space below it, since the container was taller
  // than the visible area and centered within THAT, not the visible
  // viewport. Measuring this container's own top offset and subtracting it
  // from 100dvh closes that gap regardless of Grafana's actual chrome height.
  const containerRef = useRef<HTMLDivElement>(null);
  const [containerHeight, setContainerHeight] = useState('100dvh');
  useLayoutEffect(() => {
    const measure = () => {
      const top = containerRef.current?.getBoundingClientRect().top ?? 0;
      setContainerHeight(`calc(100dvh - ${top}px)`);
    };
    measure();
    window.addEventListener('resize', measure);
    return () => window.removeEventListener('resize', measure);
  }, []);
  return (
    <div className={styles.container} style={{ height: containerHeight, overflow: 'hidden' }} ref={containerRef}>
      <div className={styles.landingContainer} style={{ height: '100%' }}>
        <button
          type="button"
          className={styles.settingsButton}
          data-testid="settings-button"
          title="Plugin configuration"
          aria-label="Plugin configuration"
          onClick={() => { window.location.href = `/plugins/${PLUGIN_ID}?page=configuration`; }}
        >
          <Icon name="cog" size="lg" />
        </button>
        <div className={styles.landingContent}>
          <div className={styles.logo}>
            <img
              src={`public/plugins/${PLUGIN_ID}/img/logo-pixo-large.png`}
              alt="Agent AI"
              // Deliberately smaller than the normal landing logo
              // (styles.logoImage, sized for the front-facing fox) -- the
              // sleeping-fox art reads as oversized at that same size.
              style={{ width: '280px', height: '280px', objectFit: 'contain' }}
              draggable={false}
              onContextMenu={(e) => e.preventDefault()}
            />
          </div>
          <Alert title="Under maintenance" severity="info" style={{ maxWidth: '480px', flex: 'none' }}>
            Agent AI is currently under maintenance. Please try again later.
          </Alert>
        </div>
      </div>
    </div>
  );
}

function AppRoot(props: AppRootProps) {
  const path = props.path || window.location.pathname;
  const [limits, setLimits] = useState<Limits>(DEFAULT_LIMITS);
  useEffect(() => {
    fetchLimits()
      .then((l) => { cachedLimits = l; setLimits(l); })
      .catch(() => {});
  }, []);

  if (path.includes('agents')) {
    return <AgentsPage />;
  }
  // Chat is the one true entry point for this app -- any other path
  // (including the bare plugin root) lands here.
  if (!limits.enableStandaloneChat) {
    return <DisabledNotice feature="Standalone chat" />;
  }
  if (limits.maintenanceMode) {
    return <MaintenanceNotice />;
  }
  return <ChatInterface responseLanguage={normalizeResponseLanguage(limits.responseLanguage)} />;
}

export const plugin = new AppPlugin<{}>()
  .setRootPage(AppRoot)
  .addConfigPage({
    title: 'Configuration',
    icon: 'cog',
    body: AppConfig,
    id: 'configuration',
  })
  .addLink({
    title: '✨ Agent AI',
    description: 'Open the Grafana assistant',
    targets: [PluginExtensionPoints.CommandPalette],
    icon: 'ai-sparkle',
    path: `/a/${PLUGIN_ID}/chat`,
    configure: () => (cachedLimits.enableStandaloneChat ? {} : undefined),
  })
  .addLink<PluginExtensionPanelContext>({
    // Grafana's panel-menu "Extensions" submenu doesn't render the `icon`
    // prop for per-plugin items (confirmed empirically) -- a symbol in the
    // title itself is the only reliable way to make this read as "AI" at a
    // glance, not just by name.
    //
    // Deliberately a DIFFERENT title than the command-palette addLink below
    // (which is "✨ Agent AI") -- Grafana's own extension validator
    // (isAddedLinkMetaInfoMissing in validators.ts) groups a plugin's
    // plugin.json addedLinks entries by title, not by target, before
    // comparing descriptions. Two links sharing one title but declaring
    // different descriptions (correctly, since they serve different
    // purposes) makes that title-grouped comparison find a "mismatch"
    // against itself and log a false-positive console warning on every
    // plugin load -- confirmed by reading Grafana's own source, not a bug
    // in this plugin's own plugin.json/module.tsx (which already matched
    // byte-for-byte). Giving this link its own title sidesteps the
    // mis-grouping entirely.
    title: '✨ Ask Agent AI',
    description: 'Ask the assistant about this panel -- uses the same chat and the same real tools (query_prometheus, query_loki, get_dashboard, etc.)',
    targets: [PluginExtensionPoints.DashboardPanelMenu],
    icon: 'ai-sparkle',
    configure: () => (cachedLimits.enableDashboardIntegration ? {} : undefined),
    onClick: (_event, helpers) => {
      // Real, live-reproduced bug: closing this modal and reopening it
      // (same panel or a different one) faster than ~2s repeatedly froze
      // the tab for 10+ seconds -- confirmed via backend logs that only
      // ONE real chat request actually reached the server each time (so
      // it isn't overlapping network requests, see ChatInterface.tsx's own
      // panelPreviewRequestInFlight guard for that separate issue); the
      // freeze happens purely from mounting/unmounting ChatInterface's own
      // React tree (30+ effects, per-render emotion CSS generation) faster
      // than the browser can settle between cycles. No natural, deliberate
      // use of this menu item is anywhere near this fast -- opening the
      // panel menu, then Extensions, then this item already takes a couple
      // of real seconds -- so a hard minimum gap between opens is a safe,
      // unintrusive guard against the specific pattern that triggers it.
      const now = Date.now();
      if (now - lastPanelPreviewOpenedAt < PANEL_PREVIEW_REOPEN_COOLDOWN_MS) {
        return;
      }
      lastPanelPreviewOpenedAt = now;
      const panelContext = helpers.context;
      helpers.openModal({
        title: '✨ Agent AI',
        body: ({ onDismiss }) => (
          <ChatInterface
            panelContext={panelContext}
            onDismiss={onDismiss}
            responseLanguage={normalizeResponseLanguage(cachedLimits.responseLanguage)}
          />
        ),
        width: '900px',
        // No fixed height: a read-only preview with one auto-sent question
        // has no reason to reserve 80vh of empty gray space up front -- let
        // it size to its actual content (containerPreview caps it at 80vh
        // and scrolls beyond that) so it only grows as the answer streams in.
      });
    },
  });
