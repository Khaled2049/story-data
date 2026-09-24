"""Only new migrations may be introduced by a pull request."""
import os
import subprocess
import sys

base = os.environ["BASE_SHA"]
head = os.environ["HEAD_SHA"]
# Compare the contributor's changes from the merge base; do not attribute newer
# migrations on main to the PR. No rename detection: renaming is a deletion.
result = subprocess.check_output(
    ["git", "diff", "--name-status", "--no-renames", "--diff-filter=DMRTUXB",
     f"{base}...{head}", "--", "migrations/"], text=True
)
if result:
    print("Existing migrations must not be changed or deleted:\n" + result)
    sys.exit(1)
