"""Domain models for the sample app."""
from dataclasses import dataclass

@dataclass
class Todo:
    id: int
    title: str
    done: bool = False
    def render(self):
        mark = "x" if self.done else " "
        return f"[{mark}] {self.id}. {self.title}"
