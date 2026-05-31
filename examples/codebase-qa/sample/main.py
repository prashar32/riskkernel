"""Entrypoint for the sample todo CLI."""
from store import TodoStore
from config import load_config

def main():
    cfg = load_config()
    store = TodoStore(path=cfg.db_path)
    store.add("write the RiskKernel demo")
    for t in store.list():
        print(t.render())

if __name__ == "__main__":
    main()
