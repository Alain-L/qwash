#!/bin/bash
# Recursively scans all .go files and displays struct and function/method declarations in a readable format.

find . -type f -name '*.go' | sort | while read file; do
    echo "$file"
    grep -E '^(type [^ ]+ struct|func \([^)]+\) [^ ]+|func [^ ]+)' "$file" | \
        sed -E \
            -e 's/^type ([^ ]+) struct.*/    struct \1/' \
            -e 's/^func \(([^)]+)\) ([^(]+)\(.*/    method \2 (receiver: \1)/' \
            -e 's/^func ([^(]+)\(.*/    function \1/'
    echo
done
