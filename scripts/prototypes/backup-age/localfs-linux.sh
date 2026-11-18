#!/bin/sh
# Synthetic filesystem experiment; run only in a disposable non-root container.
set -eu
cd /prototype
test "$(id -u)" = 10001
umask 077
mkdir private
test "$(stat -c %a private)" = 700
echo 'PASS private directory mode 0700'
printf '%s' 'synthetic only' > private/plain.txt
test "$(stat -c %a private/plain.txt)" = 600
test "$(stat -c %u private/plain.txt)" = 10001
echo 'PASS plaintext file mode 0600 and non-root ownership'
mkdir permissive
chmod 755 permissive
test "$(stat -c %a permissive)" != 700
echo 'PASS permissive directory detected'
ln -s private alias
test -L alias
test "$(readlink -f alias/plain.txt)" = "$(readlink -f private/plain.txt)"
echo 'PASS symlink alias identified'
no_links() {
    candidate=$1
    while test "$candidate" != .; do
        if test -L "$candidate"; then return 1; fi
        candidate=$(dirname "$candidate")
    done
}
if no_links alias/plain.txt; then exit 1; fi
echo 'PASS file ancestor symlink detected'
ln private/plain.txt private/hard.txt
test "$(stat -c %h private/plain.txt)" = 2
echo 'PASS hard-link alias detected by link count'
rm private/hard.txt
test "$(stat -c %h private/plain.txt)" = 1
echo 'PASS hard-link removal preserves original'
(set -C; printf '%s' complete > private/complete)
if (set -C; printf '%s' overwrite > private/complete) 2>/dev/null; then exit 1; fi
test "$(cat private/complete)" = complete
echo 'PASS exclusive publication refuses overwrite'
rm alias
test "$(cat private/plain.txt)" = 'synthetic only'
echo 'PASS symlink removal preserves target'
