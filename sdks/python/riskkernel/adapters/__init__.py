"""Framework adapters that bind a RiskKernel governed run to popular agent
frameworks. Each adapter lazily imports its framework, so the core SDK has no
third-party dependencies and you only pay for what you use.

- ``langchain``      — a CallbackHandler (loop/time enforcement per LLM call).
- ``claude_agent``   — a PreToolUse hook for the Claude Agent SDK (approval gate).
- ``openai_agents``  — RunHooks for the OpenAI Agents SDK (steps + approval gate).
- ``crewai``         — a step_callback for CrewAI (steps + tool approval gate).
"""
