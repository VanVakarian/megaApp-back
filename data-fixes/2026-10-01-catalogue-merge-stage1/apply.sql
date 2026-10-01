.bail on
.echo on
.timeout 10000
BEGIN IMMEDIATE;
.read merge.sql
COMMIT;
