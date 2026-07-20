Intro paragraph before the code.

```python
def handler(event):
    # long enough to force a split
    return normalize(event, strict=True)

def normalize(event, strict):
    return {"name": event.name, "strict": strict}
```

Tail paragraph after the code block ends.
