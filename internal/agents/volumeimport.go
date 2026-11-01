package agents

import (
	"errors"
	"fmt"
	"strings"
)

// The restore helper mounts the volume at /dest. A replace restore
// extracts into a staging directory next to the old contents and swaps
// only once tar has succeeded, so a broken stream or a failing tar never
// leaves the volume empty or half-written:
//
//  1. move every old entry into the old directory,
//  2. create the swap marker,
//  3. move every new entry from staging into /dest,
//  4. drop the old directory (renamed to trash first).
//
// A helper killed in between leaves state that importRecover resolves
// before the next import, and that the server and agent run right after
// a failed one: without the marker the old entries go back, with it the
// new ones are moved in.
const (
	importDest    = "/dest"
	importStaging = "/dest/.dockmesh-restore-new"
	importOld     = "/dest/.dockmesh-restore-old"
	importMarker  = "/dest/.dockmesh-restore-swap"
	importTrash   = "/dest/.dockmesh-restore-trash"
)

// Exit codes of the import script beyond tar's own.
const (
	importExitNoSpace  = 3
	importExitRecover  = 4
	importExitSwap     = 5
	importExitStageDir = 6
)

// importFuncs defines mvall (move every entry of $1 except the restore's
// own paths into $2; the globs cover dotfiles and skip . and ..) and
// recover.
const importFuncs = `D=` + importDest + ` S=` + importStaging + ` O=` + importOld + ` M=` + importMarker + ` T=` + importTrash + `
mvall() {
  for f in "$1"/..?* "$1"/.[!.]* "$1"/*; do
    [ -e "$f" ] || [ -L "$f" ] || continue
    case "$f" in "$S"|"$O"|"$M"|"$T") continue;; esac
    mv -f "$f" "$2"/ || return 1
  done
}
recover() {
  if [ -d "$O" ]; then
    if [ -e "$M" ]; then
      mvall "$S" "$D" || return 1
      mv "$O" "$T" || return 1
    else
      mvall "$O" "$D" || return 1
      rmdir "$O" || return 1
    fi
  fi
  rm -rf "$S" "$T" && rm -f "$M"
}
`

// VolumeImportScript is the helper command that extracts a restore into
// /dest. replace swaps the volume's contents for the archive's; otherwise
// the archive is merged. needBytes, when above 0, is the free space a
// replace needs while old and new contents exist side by side.
func VolumeImportScript(replace bool, needBytes int64) string {
	var b strings.Builder
	b.WriteString(importFuncs)
	fmt.Fprintf(&b, "recover || { echo 'an earlier restore into this volume was interrupted and could not be undone; its files are in .dockmesh-restore-old and .dockmesh-restore-new' >&2; exit %d; }\n", importExitRecover)
	if !replace {
		b.WriteString("exec tar xzf - -C \"$D\"\n")
		return b.String()
	}
	if needKB := (needBytes + 1023) / 1024; needKB > 0 {
		fmt.Fprintf(&b, `avail=$(df -Pk "$D" | awk 'NR==2 {print $4}')
if [ -n "$avail" ] && [ "$avail" -lt %d ]; then echo "not enough free space in the volume: need %d KiB, $avail KiB free" >&2; exit %d; fi
`, needKB, needKB, importExitNoSpace)
	}
	fmt.Fprintf(&b, `mkdir "$S" || exit %d
if ! tar xzf - -C "$S"; then rm -rf "$S"; exit 1; fi
if ! { mkdir "$O" && mvall "$D" "$O" && touch "$M" && mvall "$S" "$D"; }; then recover; exit %d; fi
chown "$(stat -c %%u:%%g "$S")" "$D" 2>/dev/null; chmod "$(stat -c %%a "$S")" "$D" 2>/dev/null
rmdir "$S" && mv "$O" "$T" && rm -f "$M" && rm -rf "$T"
`, importExitStageDir, importExitSwap)
	return b.String()
}

// VolumeRecoverScript resolves what an interrupted import left in /dest:
// the old contents come back unless the swap had already started.
func VolumeRecoverScript() string {
	return importFuncs + "recover\n"
}

// VolumeImportExitError explains an exit code of VolumeImportScript.
func VolumeImportExitError(code int64, replace bool) error {
	switch {
	case code == 0:
		return nil
	case !replace && code != importExitRecover:
		return fmt.Errorf("restore tar exit %d", code)
	}
	switch code {
	case importExitNoSpace:
		return errors.New("not enough free space in the volume for a replace restore (old and new contents are kept side by side until the swap); nothing was changed")
	case importExitRecover:
		return errors.New("an earlier restore into this volume was interrupted and could not be undone; nothing was changed")
	case importExitSwap:
		return errors.New("swapping the restored files in failed; the previous contents were put back")
	case importExitStageDir:
		return errors.New("could not create the staging directory in the volume; nothing was changed")
	}
	return fmt.Errorf("restore tar exit %d; the volume keeps its previous contents", code)
}
