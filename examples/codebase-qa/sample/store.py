"""In-memory todo store (the sample app's data layer)."""
from models import Todo

class TodoStore:
    def __init__(self, path):
        self.path = path
        self._items = []
    def add(self, title):
        self._items.append(Todo(id=len(self._items) + 1, title=title))
    def list(self):
        return list(self._items)
