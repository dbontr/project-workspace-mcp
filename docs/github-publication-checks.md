# GitHub publication checks

Workspace MCP publishes a revision-guarded commit through its controller-owned GitHub credential. The caller supplies the expected branch head. If the head changes, publication must fail without rewriting the remote branch. Merge operations must validate the selected pull request's head and required checks; branch deletion occurs only after a confirmed merge.
