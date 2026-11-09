#!/bin/sh
# MCP stdio server over opwork; see /usr/local/lib/opwork/opwork_mcp.py. Same client, same token route.
exec python3 -I -B /usr/local/lib/opwork/opwork_mcp.py "$@"
