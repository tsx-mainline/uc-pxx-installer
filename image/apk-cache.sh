#!/bin/bash
# SPDX-License-Identifier: GPL-2.0-or-later
# The pinned Alpine packages of the installer RAM image.
#
#   image/apk-cache.sh fetch  --cache DIR [--lock FILE] [--mirror URL]
#   image/apk-cache.sh verify --cache DIR [--lock FILE] [--stage DIR]
#   image/apk-cache.sh lock   --cache DIR [--mirror URL] > NEW.lock
#
# image/apk.lock names every file that the image build reads from Alpine: the
# signed index (APKINDEX.tar.gz) of main and of community, and each package,
# with its version and sha256. image/build.sh reads these files only from a
# local cache and never from the network (its containers have no network).
#
#   fetch   downloads the files of the lock from the mirror into the cache
#           and checks the sha256 of each one (needs the network). A file that
#           the mirror does not have any more, or a file with another sha256,
#           is an error. Alpine removes old package versions and changes its
#           indexes, so keep the cache of each release: it is an input of the
#           image, as the source is
#   verify  checks the cache against the lock (no network). With --stage DIR
#           it copies exactly the files of the lock into the new folder DIR,
#           in the layout of a mirror. image/build.sh gives DIR to the Alpine
#           container as its only repository
#   lock    makes a new lock from the mirror as it is today (needs the
#           network). It downloads the two indexes into the cache, lets apk
#           resolve the packages of image/mkinitramfs.sh against them,
#           downloads those packages and prints the lock. A new lock changes
#           the image: commit it, build, and review the result
#
# Lock lines (fields split by spaces):
#   alpine BRANCH ARCH                     once, first
#   index REPO - - SHA256                  BRANCH/REPO/ARCH/APKINDEX.tar.gz
#   root REPO NAME VERSION SHA256          installed into the image root
#   tool REPO NAME VERSION SHA256          installed into the build container
# A package file is BRANCH/REPO/ARCH/NAME-VERSION.apk in the cache.
#
# Env: ALPINE_IMAGE (default arm64v8/alpine:3.24), for fetch and lock.
set -euo pipefail
HERE=$(cd "$(dirname "$0")/.." && pwd)
MODE=${1:-} CACHE= LOCK=$HERE/image/apk.lock STAGE= MIRROR=https://dl-cdn.alpinelinux.org/alpine
usage() { sed -n '5,7p' "$0" >&2; exit 2; }
[ $# -gt 0 ] && shift
while [ $# -gt 0 ]; do
	case $1 in
	--cache) CACHE=${2:?}; shift;;
	--lock) LOCK=${2:?}; shift;;
	--stage) STAGE=${2:?}; shift;;
	--mirror) MIRROR=${2:?}; shift;;
	*) usage;;
	esac
	shift
done
[ -n "$CACHE" ] || usage
ALPINE_IMAGE=${ALPINE_IMAGE:-arm64v8/alpine:3.24}
die() { echo "apk-cache: $*" >&2; exit 1; }

# The packages of image/mkinitramfs.sh. mkinitramfs.sh checks that it
# installed exactly the root lines of the lock.
ROOT_PKGS="alpine-baselayout busybox busybox-binsh busybox-extras dropbear"
TOOL_PKGS="font-terminus cpio"

# read_lock: check the lock and print one line per file: SHA256 PATH.
read_lock() {
	[ -f "$LOCK" ] || die "no lock file $LOCK"
	awk '
	function bad(m) { printf "apk-cache: %s line %d: %s\n", FILENAME, NR, m > "/dev/stderr"; err = 1; exit 1 }
	/^#/ || /^$/ { next }
	{
		if ($0 ~ /\r/ || $0 ~ /\t/) bad("tab or CR")
		if (!hdr) {
			if (NF != 3 || $1 != "alpine" || $2 !~ /^v[0-9]+\.[0-9]+$/ || $3 !~ /^[a-z0-9_]+$/) bad("the first line must be: alpine BRANCH ARCH")
			hdr = 1; b = $2; a = $3; next
		}
		if (NF != 5) bad("5 fields expected")
		if ($2 != "main" && $2 != "community") bad("repo must be main or community")
		if ($5 !~ /^[0-9a-f]{64}$/) bad("bad sha256")
		if ($1 == "index") {
			if ($3 != "-" || $4 != "-") bad("an index line has - - as name and version")
			p = b "/" $2 "/" a "/APKINDEX.tar.gz"
		} else if ($1 == "root" || $1 == "tool") {
			if ($3 !~ /^[a-z0-9][a-z0-9._+-]*$/ || $4 !~ /^[0-9][a-z0-9._-]*-r[0-9]+$/) bad("bad name or version")
			p = b "/" $2 "/" a "/" $3 "-" $4 ".apk"
		} else bad("unknown kind " $1)
		if (p in seen) bad("file listed twice: " p)
		seen[p] = 1; n++
		print $5 "  " p
	}
	END { if (!err && (!hdr || n == 0)) { print "apk-cache: " FILENAME ": empty lock" > "/dev/stderr"; exit 1 } }
	' "$LOCK"
}

case $MODE in
verify)
	list=$(read_lock) || exit 1
	[ -d "$CACHE" ] || die "no cache folder $CACHE (image/apk-cache.sh fetch --cache $CACHE)"
	(cd "$CACHE" && sha256sum --quiet --strict -c) <<< "$list" ||
		die "the cache $CACHE does not match $LOCK: a file is missing or differs (image/apk-cache.sh fetch)"
	if [ -n "$STAGE" ]; then
		[ ! -e "$STAGE" ] || [ -z "$(ls -A "$STAGE")" ] || die "--stage $STAGE is not empty"
		mkdir -p "$STAGE"
		while read -r sum p; do
			mkdir -p "$STAGE/$(dirname "$p")"
			cp "$CACHE/$p" "$STAGE/$p"
		done <<< "$list"
		# check the copies too: the container reads only these
		(cd "$STAGE" && sha256sum --quiet --strict -c) <<< "$list" || die "the copy in $STAGE differs"
	fi
	echo "apk-cache: $(wc -l <<< "$list") files match $LOCK"
	;;
fetch)
	list=$(read_lock) || exit 1
	mkdir -p "$CACHE"
	CACHE=$(cd "$CACHE" && pwd)
	# Download only what is missing or wrong, each file to a .part name first.
	need=$( (cd "$CACHE" && while read -r sum p; do
		[ -f "$p" ] && [ "$(sha256sum < "$p" | cut -c1-64)" = "$sum" ] || echo "$p"
	done) <<< "$list")
	if [ -n "$need" ]; then
		docker run --rm -i -u "$(id -u):$(id -g)" --platform linux/arm64 -v "$CACHE:/c" -e M="$MIRROR" \
			"$ALPINE_IMAGE" sh -c 'set -eu; while read -r p; do
				mkdir -p "/c/$(dirname "$p")"
				wget -q -O "/c/$p.part" "$M/$p" || { echo "apk-cache: cannot download $M/$p" >&2; rm -f "/c/$p.part"; exit 1; }
			done' <<< "$need"
		while read -r p; do
			sum=$(awk -v p="$p" '$2 == p { print $1 }' <<< "$list")
			got=$(sha256sum < "$CACHE/$p.part" | cut -c1-64)
			[ "$got" = "$sum" ] || { rm -f "$CACHE/$p.part"; die "$MIRROR/$p has sha256 $got, the lock says $sum"; }
			mv "$CACHE/$p.part" "$CACHE/$p"
		done <<< "$need"
	fi
	(cd "$CACHE" && sha256sum --quiet --strict -c) <<< "$list" || die "the cache $CACHE does not match $LOCK"
	echo "apk-cache: $(wc -l <<< "$list") files in $CACHE match $LOCK ($(grep -c . <<< "$need" || true) downloaded)"
	;;
lock)
	mkdir -p "$CACHE"
	CACHE=$(cd "$CACHE" && pwd)
	BR=v3.24 ARCH=aarch64
	# Root in the container (apk needs it), then the files go back to the user.
	res=$(docker run --rm -i --platform linux/arm64 -v "$CACHE:/c" -e M="$MIRROR" -e B=$BR -e A=$ARCH \
		-e ROOT_PKGS="$ROOT_PKGS" -e TOOL_PKGS="$TOOL_PKGS" -e UIDGID="$(id -u):$(id -g)" "$ALPINE_IMAGE" sh -s <<'EOF'
set -eu
trap 'chown -R "$UIDGID" /c' EXIT
[ "$(apk --print-arch)" = "$A" ] || { echo "apk-cache: the container is not $A" >&2; exit 1; }
for r in main community; do
	mkdir -p /c/$B/$r/$A
	wget -q -O /c/$B/$r/$A/APKINDEX.tar.gz "$M/$B/$r/$A/APKINDEX.tar.gz"
done
printf '/c/%s/main\n/c/%s/community\n' $B $B > /etc/apk/repositories
# Resolve against the two indexes just downloaded (apk checks their signature).
inst() { sed -n "s/^( *[0-9]*\/[0-9]*) Installing \([^ ]*\) (\([^)]*\))\$/$1 \1 \2/p" /tmp/sim; }
# an empty root first: --simulate does not make the database
mkdir -p /tmp/r
apk add -q --root /tmp/r --initdb --no-cache --repositories-file /etc/apk/repositories --keys-dir /etc/apk/keys
apk add --root /tmp/r --no-cache --simulate --repositories-file /etc/apk/repositories \
	--keys-dir /etc/apk/keys $ROOT_PKGS > /tmp/sim
inst root > /tmp/want
apk add --no-cache --simulate $TOOL_PKGS > /tmp/sim
inst tool >> /tmp/want
[ "$(grep -c '^root ' /tmp/want)" -ge 5 ] || { echo "apk-cache: apk resolved no root packages" >&2; exit 1; }
while read -r kind n v; do
	got=
	for r in main community; do
		if wget -q -O /c/$B/$r/$A/$n-$v.apk "$M/$B/$r/$A/$n-$v.apk" 2>/dev/null; then got=$r; break; fi
		rm -f /c/$B/$r/$A/$n-$v.apk
	done
	[ -n "$got" ] || { echo "apk-cache: cannot download $n-$v.apk" >&2; exit 1; }
	echo "$kind $got $n $v"
done < /tmp/want
EOF
	)
	echo "# image/apk.lock: the Alpine packages of the installer RAM image, pinned by"
	echo "# version and sha256. Made by image/apk-cache.sh lock on $(date -u +%Y-%m-%d)."
	echo "# Lines: alpine BRANCH ARCH; KIND REPO NAME VERSION SHA256 (see image/apk-cache.sh)."
	echo "alpine $BR $ARCH"
	for r in main community; do
		echo "index $r - - $(sha256sum < "$CACHE/$BR/$r/$ARCH/APKINDEX.tar.gz" | cut -c1-64)"
	done
	# A package that both lists need is a root package: one line per file.
	while read -r kind r n v; do
		[ "$kind" = tool ] && grep -q "^root $r $n $v\$" <<< "$res" && continue
		echo "$kind $r $n $v $(sha256sum < "$CACHE/$BR/$r/$ARCH/$n-$v.apk" | cut -c1-64)"
	done <<< "$res"
	;;
*) usage;;
esac
