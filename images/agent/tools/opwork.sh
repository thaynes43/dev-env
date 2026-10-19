#!/bin/sh
# Shared OpenProject backlog CLI; see /usr/local/lib/opwork/opwork.py --help.
exec python3 -I -B /usr/local/lib/opwork/opwork.py "$@"
