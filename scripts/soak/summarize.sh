#!/usr/bin/env bash
# Summarizes a monitor.sh CSV: baseline (first 10 samples), peak, last 10 samples.
#   scripts/soak/summarize.sh soak.csv
set -euo pipefail
awk -F, 'NR==1 {for (i=1;i<=NF;i++) h[i]=$i; next}
{ n++; for (i=2;i<=NF;i++) { v=$i+0; if (n<=10) b[i]+=v; if (v>p[i]) p[i]=v; last[n%10,i]=v } }
END {
  printf "%-42s %16s %16s %16s\n", "metric", "baseline(avg10)", "peak", "final(avg10)";
  for (i=2;i<=NF;i++) { f=0; for (k=0;k<10;k++) f+=last[k,i];
    printf "%-42s %16.0f %16.0f %16.0f\n", h[i], b[i]/10, p[i], f/10 } }' "$1"
