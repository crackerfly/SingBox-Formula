#!/bin/sh
set -eux

: "------"
: "CROSS_TRIPLET=$CROSS_TRIPLET"
: "CROSS_TOOLCHAIN_URL=$CROSS_TOOLCHAIN_URL"
: "CROSS_TOOLCHAIN_SHA256=$CROSS_TOOLCHAIN_SHA256"
: "CROSS_DIR=$CROSS_DIR"
: "------"

CROSS_BASE="$CROSS_DIR/$CROSS_TRIPLET"
CROSS_SRC="$CROSS_BASE/src"
CROSS_USR="$CROSS_BASE/$CROSS_TRIPLET/sysroot/usr"
PATH="$CROSS_BASE/bin:$PATH"
PKG_CONFIG_PATH="$CROSS_DIR/$CROSS_TRIPLET/$CROSS_TRIPLET/sysroot/usr/lib/pkgconfig"
export PATH PKG_CONFIG_PATH

libmnl_name=libmnl-1.0.5
libmnl_tar=$libmnl_name.tar.bz2
libmnl_sha256=274b9b919ef3152bfb3da3a13c950dd60d6e2bcd54230ffeca298d03b40d0525
libmnl_url="https://www.netfilter.org/projects/libmnl/files/$libmnl_tar"

libnfnetlink_name=libnfnetlink-1.0.2
libnfnetlink_tar=$libnfnetlink_name.tar.bz2
libnfnetlink_sha256=b064c7c3d426efb4786e60a8e6859b82ee2f2c5e49ffeea640cfe4fe33cbc376
libnfnetlink_url="https://www.netfilter.org/projects/libnfnetlink/files/$libnfnetlink_tar"

libnetfilter_queue_name=libnetfilter_queue-1.0.5
libnetfilter_queue_tar=$libnetfilter_queue_name.tar.bz2
libnetfilter_queue_sha256=f9ff3c11305d6e03d81405957bdc11aea18e0d315c3e3f48da53a24ba251b9f5
libnetfilter_queue_url="https://www.netfilter.org/projects/libnetfilter_queue/files/$libnetfilter_queue_tar"


: "Downloading toolchain..."
cd "$CROSS_DIR"
curl -Lfo "$CROSS_TRIPLET.tar.xz" "$CROSS_TOOLCHAIN_URL"
echo "$CROSS_TOOLCHAIN_SHA256 *$CROSS_TRIPLET.tar.xz" | sha256sum -c
sha256sum "$CROSS_TRIPLET.tar.xz"


: "Extracting toolchain..."
cd "$CROSS_DIR"
rm -rf "$CROSS_BASE"
tar xf "$CROSS_TRIPLET.tar.xz"
chmod -R u+w "$CROSS_BASE"
mkdir -p "$CROSS_SRC"


: "Downloading dependencies..."
cd "$CROSS_SRC"
curl -Lfo "$libmnl_tar" "$libmnl_url"
echo "$libmnl_sha256 *$libmnl_tar" | sha256sum -c
curl -Lfo "$libnfnetlink_tar" "$libnfnetlink_url"
echo "$libnfnetlink_sha256 *$libnfnetlink_tar" | sha256sum -c
curl -Lfo "$libnetfilter_queue_tar" "$libnetfilter_queue_url"
echo "$libnetfilter_queue_sha256 *$libnetfilter_queue_tar" | sha256sum -c


: "Building libmnl..."
cd "$CROSS_SRC"
rm -rf "$libmnl_name"
tar xf "$libmnl_tar"
mkdir "$libmnl_name/builddir"
cd "$libmnl_name/builddir"
../configure \
    --enable-static \
    --host="$CROSS_TRIPLET" \
    --prefix="$CROSS_USR"
make
make install


: "Building libnfnetlink..."
cd "$CROSS_SRC"
rm -rf "$libnfnetlink_name"
tar xf "$libnfnetlink_tar"
mkdir "$libnfnetlink_name/builddir"
cd "$libnfnetlink_name/builddir"
../configure \
    --enable-static \
    --host="$CROSS_TRIPLET" \
    --prefix="$CROSS_USR"
make
make install


: "Building libnetfilter_queue..."
cd "$CROSS_SRC"
rm -rf "$libnetfilter_queue_name"
tar xf "$libnetfilter_queue_tar"
mkdir "$libnetfilter_queue_name/builddir"
cd "$libnetfilter_queue_name/builddir"
../configure \
    --enable-static \
    --host="$CROSS_TRIPLET" \
    --prefix="$CROSS_USR"
make
make install
