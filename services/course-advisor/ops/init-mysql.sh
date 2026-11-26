#!/bin/sh
set -eu
case "$ADVISOR_LAB_MARKER" in
    ''|*[!0-9a-f]*) echo "invalid lab marker" >&2; exit 1 ;;
esac
if [ "${#ADVISOR_LAB_MARKER}" -ne 64 ]; then
    echo "invalid lab marker length" >&2
    exit 1
fi
MYSQL_PWD="$MYSQL_ROOT_PASSWORD" mysql -uroot --protocol=socket --database=advisor_phase6_lab <<-SQL
    CREATE TABLE phase6_lab_identity (
        id TINYINT PRIMARY KEY,
        token CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL
    ) ENGINE=InnoDB;
    INSERT INTO phase6_lab_identity VALUES (1, '${ADVISOR_LAB_MARKER}');
SQL
