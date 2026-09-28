#!/system/bin/sh
# Orb's Linux: Termux's bash, inside proot, over the base system the app installed in $ORB_LINUX.
# Shipped as liblinux.so next to proot; Orb's bash tool and the terminal both start it like a shell
# (liblinux.so -c "command", liblinux.so -l). Termux's packages expect /data/data/com.termux: proot
# puts the app's copy there, and /usr too, so scripts written for any Linux find their tools.
lib=${0%/*}
root=${ORB_LINUX:-$HOME/linux}
# Inside proot the app's files read as /data/data/…: HOME spelled the same way stays ~ in prompts.
case $root in /data/user/0/*) root=/data/data/${root#/data/user/0/} ;; esac
export ORB_LINUX="$root"
usr=/data/data/com.termux/files/usr
export PROOT_LOADER="$lib/libprootloader.so" PROOT_TMP_DIR="$root/files/usr/tmp" LD_LIBRARY_PATH="$lib"
export PREFIX="$usr" TMPDIR="$usr/tmp" LANG="${LANG:-en_US.UTF-8}" TERM="${TERM:-xterm-256color}"
export PATH="$usr/bin:$PATH" HOME="$root/files/home"
[ -d "$PROOT_TMP_DIR" ] || /system/bin/mkdir -p "$PROOT_TMP_DIR"
storage=
[ -r /storage/emulated/0 ] && storage="-b /storage -b /sdcard"
# shellcheck disable=SC2086
exec "$lib/libproot.so" --kill-on-exit -b "$root:/data/data/com.termux" -b "$root/files/usr:/usr" $storage "$usr/bin/bash" "$@"
