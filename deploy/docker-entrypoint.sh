#!/bin/sh
set -eu
umask 077
case "${1:-}" in
    maintain|worker|run)
        exec xvfb-run -a -s '-screen 0 1280x900x24 -nolisten tcp' /app/bin/accountpool "$@"
        ;;
    *) exec /app/bin/accountpool "$@" ;;
esac
