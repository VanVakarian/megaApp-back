.bail on
.echo on
.timeout 10000
BEGIN IMMEDIATE;
.read fix.sql
ROLLBACK;
