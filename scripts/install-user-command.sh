#!/usr/bin/env sh
set -eu

usage() {
  cat <<'EOF'
Usage:
  ./scripts/install-user-command.sh [binary]
  ./scripts/install-user-command.sh [--bin-dir DIR] [--name NAME] [--no-path] [binary]

Install the MCPX binary as a user-level command without sudo.

Defaults:
  binary    <repo>/bin/mcpx
  bin dir   $XDG_BIN_HOME, or $HOME/.local/bin
  name      mcpx

Options:
  --bin-dir DIR  Override the user command directory.
  --name NAME    Override the installed command name.
  --no-path      Do not update the shell profile when the bin dir is missing from PATH.
  -h, --help     Show this help.
EOF
}

fail() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

case "${HOME:-}" in
  '') fail 'HOME is not set' ;;
esac

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd -P)
REPO_ROOT=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd -P)

INSTALL_DIR=${XDG_BIN_HOME:-"$HOME/.local/bin"}
COMMAND_NAME=mcpx
UPDATE_PATH=1
SOURCE=

while [ "$#" -gt 0 ]; do
  case "$1" in
    --bin-dir)
      [ "$#" -ge 2 ] || fail '--bin-dir requires a directory'
      INSTALL_DIR=$2
      shift 2
      ;;
    --name)
      [ "$#" -ge 2 ] || fail '--name requires a command name'
      COMMAND_NAME=$2
      shift 2
      ;;
    --no-path)
      UPDATE_PATH=0
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    --)
      shift
      [ "$#" -le 1 ] || fail 'only one binary path may be provided'
      if [ "$#" -eq 1 ]; then
        SOURCE=$1
      fi
      break
      ;;
    -*)
      fail "unknown option: $1"
      ;;
    *)
      [ -z "$SOURCE" ] || fail 'only one binary path may be provided'
      SOURCE=$1
      shift
      ;;
  esac
done

if [ -z "$SOURCE" ]; then
  SOURCE="$REPO_ROOT/bin/mcpx"
fi

case "$COMMAND_NAME" in
  ''|*/*) fail 'command name must be a non-empty basename' ;;
esac

[ -f "$SOURCE" ] || fail "binary not found: $SOURCE"
[ -x "$SOURCE" ] || chmod u+x "$SOURCE"

mkdir -p "$INSTALL_DIR"
INSTALL_DIR=$(CDPATH= cd -- "$INSTALL_DIR" && pwd -P)
DESTINATION="$INSTALL_DIR/$COMMAND_NAME"
TMP_DESTINATION=$(mktemp "$INSTALL_DIR/.${COMMAND_NAME}.XXXXXX")
trap 'rm -f "$TMP_DESTINATION"' EXIT HUP INT TERM

cp "$SOURCE" "$TMP_DESTINATION"
chmod 0755 "$TMP_DESTINATION"
mv -f "$TMP_DESTINATION" "$DESTINATION"
trap - EXIT HUP INT TERM

PROFILE=
PATH_LINE=
case ":${PATH:-}:" in
  *":$INSTALL_DIR:"*) ;;
  *)
    if [ "$UPDATE_PATH" -eq 1 ]; then
      case "${SHELL:-}" in
        */zsh)
          PROFILE="$HOME/.zshrc"
          PATH_LINE="export PATH=\"$INSTALL_DIR:\$PATH\""
          ;;
        */bash)
          if [ -f "$HOME/.bash_profile" ]; then
            PROFILE="$HOME/.bash_profile"
          else
            PROFILE="$HOME/.bashrc"
          fi
          PATH_LINE="export PATH=\"$INSTALL_DIR:\$PATH\""
          ;;
        */fish)
          PROFILE="$HOME/.config/fish/config.fish"
          PATH_LINE="fish_add_path -g \"$INSTALL_DIR\""
          mkdir -p "$HOME/.config/fish"
          ;;
        *)
          PROFILE="$HOME/.profile"
          PATH_LINE="export PATH=\"$INSTALL_DIR:\$PATH\""
          ;;
      esac

      touch "$PROFILE"
      if ! grep -Fqx "$PATH_LINE" "$PROFILE"; then
        printf '\n# MCPX user commands\n%s\n' "$PATH_LINE" >> "$PROFILE"
      fi
    fi
    ;;
esac

printf 'Installed %s -> %s\n' "$COMMAND_NAME" "$DESTINATION"

if "$DESTINATION" -version >/dev/null 2>&1; then
  "$DESTINATION" -version
else
  printf 'warning: installed binary could not run with -version; verify that it matches this OS/architecture.\n' >&2
fi

case ":${PATH:-}:" in
  *":$INSTALL_DIR:"*)
    printf 'Command is ready: %s\n' "$COMMAND_NAME"
    ;;
  *)
    if [ -n "$PROFILE" ]; then
      printf 'PATH updated in %s. Open a new terminal, then run: %s\n' "$PROFILE" "$COMMAND_NAME"
    else
      printf 'Add %s to PATH, then run: %s\n' "$INSTALL_DIR" "$COMMAND_NAME"
    fi
    ;;
esac
