#!/bin/sh
printf 'Status: 200 OK\r\n'
printf 'Content-Type: text/plain; charset=utf-8\r\n'
printf '\r\n'
printf 'CGI process handled %s %s\n' "$REQUEST_METHOD" "$PATH_INFO"
