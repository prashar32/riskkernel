"""Configuration loading for the sample app."""
import os
from dataclasses import dataclass

@dataclass
class Config:
    db_path: str

def load_config():
    return Config(db_path=os.environ.get("TODO_DB", "./todos.db"))
