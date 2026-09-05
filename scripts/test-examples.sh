#!/bin/sh
set -eu

project_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
tmp_dir=$(mktemp -d)
gateway_pid=
test_port=${MINI_CLOUD_TEST_PORT:-19080}

cleanup() {
  if [ -n "$gateway_pid" ]; then
    kill "$gateway_pid" 2>/dev/null || true
    wait "$gateway_pid" 2>/dev/null || true
  fi
  rm -rf "$tmp_dir"
}
trap cleanup EXIT INT TERM

cd "$project_dir"
go build -o "$tmp_dir/mini-cloud" .
cp -R examples/apps "$tmp_dir/apps"
sed \
  -e "s|127.0.0.1:9080|127.0.0.1:$test_port|" \
  -e "s|\"apps_dir\": \"./examples/apps\"|\"apps_dir\": \"$tmp_dir/apps\"|" \
  mini-cloud.example.json >"$tmp_dir/config.json"
"$tmp_dir/mini-cloud" -config "$tmp_dir/config.json" >"$tmp_dir/gateway.log" 2>&1 &
gateway_pid=$!

i=0
while ! curl -fsS -H 'Host: admin.apps.test' "http://127.0.0.1:$test_port/" >/dev/null; do
  i=$((i + 1))
  if [ "$i" -ge 50 ]; then
    cat "$tmp_dir/gateway.log"
    exit 1
  fi
  sleep 0.1
done

curl -fsS -H 'Host: hello.apps.test' "http://127.0.0.1:$test_port/" | grep -q 'hello from the process example'
curl -fsS -H 'Host: page.apps.test' "http://127.0.0.1:$test_port/" | grep -q 'Static files are live'
curl -fsS -H 'Host: cgi.apps.test' "http://127.0.0.1:$test_port/example" | grep -q 'CGI process handled GET /example'
curl -fsS -H 'Host: admin.apps.test' "http://127.0.0.1:$test_port/api/apps" | grep -q '"Name":"hello"'
curl -fsS -H 'Host: apps.test' "http://127.0.0.1:$test_port/" | grep -q 'Static page'

printf 'example smoke tests passed\n'
