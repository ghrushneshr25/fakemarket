# Profile artifacts

```text
out/<git-commit>/
  A_mixed_{cpu,heap,allocs,mutex,block}.pprof
  B_hot_*.pprof
  C_fullmatch_*.pprof
  D_wal_*.pprof
  E_depth_*.pprof
  *_env.txt
```

Generate: `make profile`  
Summarize: `make profile-top`  
Findings: [../PROFILING.md](../PROFILING.md)

`.pprof` binaries are gitignored (size + machine-specific). Re-run to refresh.
