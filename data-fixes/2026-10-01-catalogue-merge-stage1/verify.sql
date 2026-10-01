-- Independent post-merge verification. Does not reuse anything from merge.sql: the mapping is
-- derived from the data and compared with a separately typed expected list.
-- Expects: main = database AFTER the merge, `b` = attached database BEFORE it
-- (a pre-change backup on the server, an in-memory snapshot in the local rehearsal).
-- Read-only.

CREATE TEMP TABLE expectedMap (keepId INTEGER, dropId INTEGER);
INSERT INTO expectedMap VALUES
  (59,219),(220,334),(361,363),(231,233),(121,252),(121,272),(242,243),(373,372),(343,342),(380,379),
  (366,441),(148,245),(364,514),(438,459),(302,327),(476,475),(225,54),(224,58),(283,357),(75,502),(75,503);

CREATE TEMP TABLE observedMap AS
  SELECT bd.foodCatalogueId AS dropId, ad.foodCatalogueId AS keepId, count(*) AS movedRows
  FROM b.foodDiary bd JOIN main.foodDiary ad ON ad.id = bd.id
  WHERE ad.foodCatalogueId <> bd.foodCatalogueId
  GROUP BY 1, 2;

CREATE TEMP TABLE checks (name TEXT NOT NULL, ok INTEGER NOT NULL CHECK (ok = 1));

-- human-readable evidence: what moved where
SELECT 'MOVED', o.dropId || ' "' || d.name || '"', '->', o.keepId || ' "' || k.name || '"', o.movedRows || ' rows'
FROM observedMap o
JOIN b.foodCatalogue d ON d.id = o.dropId
JOIN main.foodCatalogue k ON k.id = o.keepId
ORDER BY o.keepId, o.dropId;

INSERT INTO checks VALUES
  ('observed diary mapping == expected mapping (both directions)',
    NOT EXISTS (SELECT dropId, keepId FROM observedMap EXCEPT SELECT dropId, keepId FROM expectedMap)
    AND NOT EXISTS (SELECT dropId, keepId FROM expectedMap EXCEPT SELECT dropId, keepId FROM observedMap)),
  ('removed catalogue ids == expected drop ids',
    NOT EXISTS (SELECT id FROM b.foodCatalogue EXCEPT SELECT id FROM main.foodCatalogue EXCEPT SELECT dropId FROM expectedMap)
    AND NOT EXISTS (SELECT dropId FROM expectedMap EXCEPT SELECT id FROM b.foodCatalogue)
    AND NOT EXISTS (SELECT 1 FROM main.foodCatalogue WHERE id IN (SELECT dropId FROM expectedMap))
    AND (SELECT count(*) FROM b.foodCatalogue WHERE id NOT IN (SELECT id FROM main.foodCatalogue)) = 21),
  ('no catalogue row was added', NOT EXISTS (SELECT id FROM main.foodCatalogue EXCEPT SELECT id FROM b.foodCatalogue)),
  ('diary: same ids before and after',
    (SELECT count(*) FROM b.foodDiary) = (SELECT count(*) FROM main.foodDiary)
    AND NOT EXISTS (SELECT id FROM b.foodDiary EXCEPT SELECT id FROM main.foodDiary)),
  ('diary: rows moved = rows that pointed at a drop',
    (SELECT sum(movedRows) FROM observedMap) = (SELECT count(*) FROM b.foodDiary WHERE foodCatalogueId IN (SELECT dropId FROM expectedMap))),
  ('diary: nothing but foodCatalogueId differs (full-row comparison)',
    NOT EXISTS (
      SELECT id, usersId, dateISO, foodWeight, history, ver, del FROM b.foodDiary
      EXCEPT
      SELECT id, usersId, dateISO, foodWeight, history, ver, del FROM main.foodDiary)),
  ('diary: per user row count and gram total unchanged',
    NOT EXISTS (
      SELECT usersId, count(*), sum(foodWeight) FROM b.foodDiary GROUP BY usersId
      EXCEPT
      SELECT usersId, count(*), sum(foodWeight) FROM main.foodDiary GROUP BY usersId)),
  ('diary: per user per day gram total unchanged',
    NOT EXISTS (
      SELECT usersId, dateISO, sum(foodWeight) FROM b.foodDiary GROUP BY usersId, dateISO
      EXCEPT
      SELECT usersId, dateISO, sum(foodWeight) FROM main.foodDiary GROUP BY usersId, dateISO)),
  ('diary: per user per product (after mapping) gram total preserved',
    NOT EXISTS (
      SELECT bd.usersId, COALESCE(om.keepId, bd.foodCatalogueId), sum(bd.foodWeight)
      FROM b.foodDiary bd LEFT JOIN observedMap om ON om.dropId = bd.foodCatalogueId
      GROUP BY 1, 2
      EXCEPT
      SELECT usersId, foodCatalogueId, sum(foodWeight) FROM main.foodDiary GROUP BY 1, 2)),
  ('diary: no orphans', NOT EXISTS (SELECT 1 FROM main.foodDiary WHERE foodCatalogueId NOT IN (SELECT id FROM main.foodCatalogue))),
  ('history: no orphans', NOT EXISTS (SELECT 1 FROM main.foodPersonalKcalHistory WHERE foodCatalogueId NOT IN (SELECT id FROM main.foodCatalogue))),
  ('history: rows of untouched products identical',
    NOT EXISTS (
      SELECT id, usersId, foodCatalogueId, yearMonth, kcalsPer100g, createdAt FROM b.foodPersonalKcalHistory
      WHERE foodCatalogueId NOT IN (SELECT dropId FROM expectedMap)
      EXCEPT
      SELECT id, usersId, foodCatalogueId, yearMonth, kcalsPer100g, createdAt FROM main.foodPersonalKcalHistory)),
  ('history: no row left on a drop', NOT EXISTS (SELECT 1 FROM main.foodPersonalKcalHistory WHERE foodCatalogueId IN (SELECT dropId FROM expectedMap))),
  ('history: surviving rows keep their values (id, user, month, kcal)',
    NOT EXISTS (
      SELECT id, usersId, yearMonth, kcalsPer100g FROM main.foodPersonalKcalHistory
      EXCEPT
      SELECT id, usersId, yearMonth, kcalsPer100g FROM b.foodPersonalKcalHistory)),
  ('catalogue: untouched rows identical incl. embeddings',
    NOT EXISTS (
      SELECT id, name, kcals, protein, fat, carbs, fiber, description, legacyName, nameVec, descriptionVec
      FROM b.foodCatalogue WHERE id NOT IN (SELECT dropId FROM expectedMap)
      EXCEPT
      SELECT id, name, kcals, protein, fat, carbs, fiber, description, legacyName, nameVec, descriptionVec
      FROM main.foodCatalogue)),
  ('other tables untouched: users, userSettings, foodBodyWeight, syncOperations',
    (SELECT count(*) FROM b.users) = (SELECT count(*) FROM main.users)
    AND (SELECT count(*) FROM b.userSettings) = (SELECT count(*) FROM main.userSettings)
    AND (SELECT count(*) FROM b.foodBodyWeight) = (SELECT count(*) FROM main.foodBodyWeight)
    AND (SELECT count(*) FROM b.syncOperations) = (SELECT count(*) FROM main.syncOperations));

SELECT 'VERIFY OK: ' || count(*) || ' checks' FROM checks;
SELECT 'counts', 'catalogue ' || (SELECT count(*) FROM main.foodCatalogue),
       'diary ' || (SELECT count(*) FROM main.foodDiary),
       'history ' || (SELECT count(*) FROM main.foodPersonalKcalHistory) || ' (was ' || (SELECT count(*) FROM b.foodPersonalKcalHistory) || ')';
