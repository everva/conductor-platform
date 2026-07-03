#!/usr/bin/env python3
# Assemble the audit prompt: template + project + files-list-file -> output file.
import sys
tmpl = open(sys.argv[1]).read()
proj = sys.argv[2]
files = open(sys.argv[3]).read().strip()
open(sys.argv[4], "w").write(tmpl.replace("__PROJECT__", proj).replace("__FILES__", files))
