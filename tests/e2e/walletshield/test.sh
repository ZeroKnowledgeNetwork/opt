#!/bin/bash

dir=$(dirname $(realpath "${0}"))
uri=http://localhost:7070
status=0

run_test() {
  test_dir="$1"
  test_case="${test_dir#${dir}}"
  output=$(mktemp)

  if [ -f "${test_dir}/in.json" ]; then
    curl \
      -X POST \
      -H 'Content-Type: application/json' \
      -d "$(cat ${test_dir}/in.json)" \
      --silent \
      "${uri}${test_case}" |
      jq -cS . 2>/dev/null >${output}

    if ! diff "${test_dir}/out.json" ${output} > /dev/null; then
      echo "${test_case}: ❌ failed"
      status=1
      [[ "${WRITE:-0}" == 1 ]] && cp -v ${output} "${test_dir}/out.json"
    else
      echo "${test_case}: ✅ passed"
    fi
  else
    echo "${test_case}: skipped (test data files not found)"
  fi

  rm -f ${output}
  sleep 1s
}

[[ "${WRITE:-0}" == 1 ]] && echo "Writing output."

if [ -z "${1}" ]; then
  for test_dir in $(find "${dir}" -mindepth 1 -type d)
  do
    run_test "${test_dir}"
  done
else
  run_test "${dir}$1"
fi

exit ${status}
