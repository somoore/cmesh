# Phase 0 Prompts

Store one prompt file per manual delegation. Name files with the delegation id from `phase0/delegations.jsonl`, for example:

```text
phase0/prompts/phase0-001.md
```

Each prompt should include:

- objective;
- cwd;
- worker machine;
- desired profile;
- commands to run;
- constraints;
- expected result shape;
- verification requested from the worker.

Do not include secrets in prompt files.
