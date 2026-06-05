# riskkernel (Python SDK)

The Python SDK for [RiskKernel](https://github.com/prashar32/riskkernel) — **Surface 2**, deep control over a governed agent run.

It is a **thin client** over the self-hosted RiskKernel daemon. Every deterministic
decision — budgets, loop/time halts, approval policy — happens in the Go core. The
SDK just makes governed runs ergonomic from Python. **Core install is stdlib-only**
(no third-party dependencies).

```bash
# Not on PyPI yet — install from source (the core is stdlib-only, so this is light):
pip install "git+https://github.com/prashar32/riskkernel.git#subdirectory=sdks/python"
```

## Quickstart

```python
import riskkernel as rk

rt = rk.Runtime(base_url="http://localhost:7070")  # your daemon

with rt.governed_run(name="research",
                     budget=rt.budget(dollars=1.00, loops=20, seconds=300)) as run:
    # Route your LLM client through the governing proxy so every model call is
    # metered, priced, and budget-enforced under this run:
    cfg = run.proxy_config()
    #   cfg["base_url"]  -> http://localhost:7070/v1
    #   cfg["headers"]   -> {"X-RiskKernel-Run-Id": "<run id>"}

    for _ in range(100):
        run.step()                      # raises rk.BudgetExceeded when loops/time run out
        # ... your agent reasoning + tool calls ...
        run.checkpoint("after-step", {"messages": messages})
```

When the governor halts the run (token / dollar / loop / time budget), the next
`run.step()` — or a proxied model call — raises `rk.BudgetExceeded`.

## Resume after a crash

The daemon reloads non-terminal runs on restart with the budget and usage they had
already spent, so a `SIGKILL`'d run keeps enforcing without re-spending. Reattach to
it by id with `resume_run` and pick your work back up from the last checkpoint:

```python
with rt.resume_run(run_id) as run:          # attaches; never creates or cancels
    cp = run.latest_checkpoint()            # the state you saved before the crash
    start = cp["payload"]["cursor"] if cp else 0
    for i in range(start, total):           # skip the steps you already paid for
        run.step()                          # counts against the SAME budget
        # ... your work ...
        run.checkpoint("step", {"cursor": i + 1})
```

The run resumes against whatever budget it had left, so it can't overspend by
restarting — `run.step()` still raises `rk.BudgetExceeded` at the original ceiling.

## Human-in-the-loop tools

Gate side-effecting tools on human approval (the daemon's policy decides what needs
it; the call blocks until a human resolves it via CLI / web / webhook):

```python
from riskkernel import governed_tool, ApprovalGate

@governed_tool(side_effect="write")
def write_file(path, content):
    ...                                  # only runs if approved; else rk.ApprovalDenied

# or explicitly:
gate = ApprovalGate(run)
if gate.allow("mcp://shell", side_effect="exec", arguments={"cmd": cmd}):
    run_shell(cmd)
```

## Framework adapters

Lazy-imported, so you only pay for what you use:

```python
# LangChain / LangGraph — enforces loop/time budgets per LLM call
from riskkernel.adapters.langchain import RiskKernelCallbackHandler
llm.invoke(prompt, config={"callbacks": [RiskKernelCallbackHandler(run)]})

# Claude Agent SDK — PreToolUse approval hook
from riskkernel.adapters.claude_agent import make_pre_tool_use_hook
hook = make_pre_tool_use_hook(run, side_effect_for={"Bash": "exec", "Write": "write"})

# OpenAI Agents SDK — RunHooks (steps + tool approval)
from riskkernel.adapters.openai_agents import RiskKernelRunHooks
hooks = RiskKernelRunHooks(run, gate_tools=True)
```

## Configuration

`Runtime(base_url=..., token=...)`, or the env vars `RISKKERNEL_BASE_URL` and
`RISKKERNEL_API_TOKEN` (used by the decorator/convenience API and `default_runtime()`).

## License

Apache-2.0.
