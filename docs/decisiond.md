ADR-001

Current timeout budget applies to the entire upstream
request/response lifecycle.

Pros:
- Simpler implementation
- Prevents slow upstreams from tying up connections indefinitely

Cons:
- Long-running downloads may be interrupted

Future Consideration:
- Separate:
    Dial timeout
    Header timeout
    Idle timeout
    Response streaming timeout