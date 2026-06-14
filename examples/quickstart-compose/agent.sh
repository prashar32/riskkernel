#!/bin/sh
# A deliberately runaway "agent": it just keeps calling the model through the
# RiskKernel proxy, sharing one run id, and never decides to stop on its own. The
# proxy meters each call and — once the run's loop budget is spent — refuses the
# next one with HTTP 402. The kill comes from RiskKernel, not from this script.
set -eu

PROXY="${PROXY:-http://riskkernel:7070}"
RUN_ID="quickstart-$(date +%s)" # a fresh run each time so re-runs start clean

echo "----------------------------------------------------------------"
echo " RiskKernel quickstart: a runaway agent vs a hard loop budget"
echo " loop budget = 5 model calls   (no API key — a mock LLM stands in)"
echo "----------------------------------------------------------------"
echo ""

i=1
while [ "$i" -le 20 ]; do
    code="$(curl -s -o /tmp/body -D /tmp/hdr -w '%{http_code}' \
        -X POST "$PROXY/v1/chat/completions" \
        -H 'Content-Type: application/json' \
        -H "X-RiskKernel-Run-Id: $RUN_ID" \
        -d '{"model":"gpt-4o","messages":[{"role":"user","content":"keep going"}]}')"

    if [ "$code" = "200" ]; then
        toks="$(awk 'tolower($1)=="x-riskkernel-tokens:"{gsub(/\r/,"");print $2}' /tmp/hdr)"
        cost="$(awk 'tolower($1)=="x-riskkernel-cost-usd:"{gsub(/\r/,"");print $2}' /tmp/hdr)"
        echo "  call $i  -> 200 OK    tokens=${toks:-?}  cost=\$${cost:-?}"
    else
        echo ""
        echo "  call $i  -> HTTP $code   RiskKernel HALTED the run:"
        sed 's/^/      /' /tmp/body
        echo ""
        echo ""
        echo "  The kill came from RiskKernel's deterministic loop budget — the"
        echo "  agent script never chose to stop. Point your own app at the proxy"
        echo "  and the same budget protects it. (See README: Use it for real.)"
        exit 0
    fi
    i=$((i + 1))
done

echo "ERROR: expected RiskKernel to halt the loop within 20 calls, but it did not." >&2
exit 1
