#!/usr/bin/env bash
# configure-tool-search-grafana.sh
# Configures the Agent AI plugin on the tool-search Grafana instance (port 3001)
# with the LLM API key from the main Grafana (port 3000).
#
# Usage:
#   bash scripts/configure-tool-search-grafana.sh <API_KEY> <GRAFANA_TOKEN>
#
# The API key is the same one configured in the main Grafana (port 3000).
# You can find it in Grafana UI: Administration > Plugins > Agent AI > Settings.
# The Grafana token is a service account token (glsa_...) with access to the
# plugin's API; never hardcode it, pass it at invocation time.
#
# Example:
#   bash scripts/configure-tool-search-grafana.sh "sk-proj-..." "glsa_..."

set -euo pipefail

GRAFANA_URL="${GRAFANA_URL:-http://localhost:3001}"
GRAFANA_USER="${GRAFANA_USER:-admin}"
GRAFANA_PASS="${GRAFANA_PASS:-admin}"
PLUGIN_ID="shortbobcat2735-agentai-app"

usage() {
  echo "Usage: $0 <API_KEY> <GRAFANA_TOKEN>"
  echo ""
  echo "  API_KEY        The LLM provider API key (same as configured in Grafana port 3000)."
  echo "  GRAFANA_TOKEN  Grafana service account token used by the plugin (glsa_...)."
  echo ""
  echo "Environment overrides:"
  echo "  GRAFANA_URL   (default: http://localhost:3001)"
  echo "  GRAFANA_USER  (default: admin)"
  echo "  GRAFANA_PASS  (default: admin)"
  exit 1
}

if [ $# -lt 2 ] || [ -z "$1" ] || [ -z "$2" ]; then
  usage
fi

API_KEY="$1"
GRAFANA_TOKEN="$2"

echo "Configuring Agent AI plugin on ${GRAFANA_URL} with enableToolSearch=true..."

RESPONSE=$(curl -sf -u "${GRAFANA_USER}:${GRAFANA_PASS}" \
  -X POST "${GRAFANA_URL}/api/plugins/${PLUGIN_ID}/settings" \
  -H 'Content-Type: application/json' \
  -d "{
    \"enabled\": true,
    \"pinned\": true,
    \"jsonData\": {
      \"endpointURL\": \"https://generativelanguage.googleapis.com/v1beta/openai/\",
      \"model\": \"gemini-3.6-flash\",
      \"timeoutSeconds\": 60,
      \"maxTokens\": 4096,
      \"enableStandaloneChat\": true,
      \"enableDashboardIntegration\": true,
      \"enableLLMAppIntegration\": true,
      \"enableBrainAgentTools\": false,
      \"enableInternetTools\": true,
      \"enableToolSearch\": true,
      \"lightModeForDefaultAgent\": true,
      \"chatRateLimitPerMinute\": 10,
      \"maxConcurrentChats\": 25,
      \"chatQueueWaitSeconds\": 30,
      \"chatQueueDepth\": 50,
      \"rateLimitMaxRetries\": 3,
      \"onlineSearchBackend\": \"duckduckgo\",
      \"onlineSearchMaxResults\": 5,
      \"onlineSearchTimeoutSeconds\": 6,
      \"attachmentMaxBytes\": 51200,
      \"responseLanguage\": \"english\"
    },
    \"secureJsonData\": {
      \"apiKey\": \"${API_KEY}\",
      \"grafanaToken\": \"${GRAFANA_TOKEN}\"
    }
  }")

echo "Response: ${RESPONSE}"

# Validate health
echo ""
echo "Checking plugin health..."
sleep 2
HEALTH=$(curl -sf -u "${GRAFANA_USER}:${GRAFANA_PASS}" \
  "${GRAFANA_URL}/api/plugins/${PLUGIN_ID}/resources/health" 2>/dev/null || echo '{"status":"unreachable"}')
echo "Health: ${HEALTH}"

# Verify enableToolSearch is set
TOOL_SEARCH=$(curl -sf -u "${GRAFANA_USER}:${GRAFANA_PASS}" \
  "${GRAFANA_URL}/api/plugins/${PLUGIN_ID}/settings" | \
  python3 -c "import sys,json; d=json.load(sys.stdin); print(d.get('jsonData',{}).get('enableToolSearch','NOT SET'))" 2>/dev/null)

echo ""
echo "✓ enableToolSearch: ${TOOL_SEARCH}"
echo ""
echo "Grafana tool-search instance is ready at: ${GRAFANA_URL}"
echo "Open the chat at: ${GRAFANA_URL}/a/${PLUGIN_ID}/chat"
