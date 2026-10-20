#!/usr/bin/env bash
# Stops the three housepage services (cloudflared, nginx, housepaged) and keeps them from restarting,
# including at login or boot. Run ./run.sh to bring them back. Only touches services run.sh created.
set -uo pipefail

case "$(uname -s)" in
Darwin)
	for n in cloudflared nginx housepaged; do
		label=dev.housepage.$n
		if launchctl bootout "gui/$UID/$label" 2>/dev/null; then echo "stopped $n"; else echo "$n was not running"; fi
		launchctl disable "gui/$UID/$label" # otherwise launchd would load it again at the next login
	done
	;;
Linux)
	for n in cloudflared nginx housepaged; do
		if systemctl --user is-active --quiet "housepage-$n.service"; then echo "stopping $n"; else echo "$n was not running"; fi
		systemctl --user disable --now "housepage-$n.service" 2>/dev/null
	done
	;;
*)
	echo "unsupported OS: $(uname -s)" >&2
	exit 1
	;;
esac
