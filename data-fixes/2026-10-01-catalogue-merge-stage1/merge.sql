-- Catalogue cleanup, stage 1 (plans/37): merge duplicate products into one.
-- Body only: the caller owns BEGIN / COMMIT / ROLLBACK (see apply.sql, rehearse.sql).
-- Every assertion inserts into a CHECK-guarded table: a violated assertion raises an error,
-- the sqlite3 shell (.bail on) stops, and the open transaction is never committed.

CREATE TEMP TABLE mergeMap (
  keepId   INTEGER NOT NULL,
  keepName TEXT    NOT NULL,
  dropId   INTEGER NOT NULL PRIMARY KEY,
  dropName TEXT    NOT NULL
);

INSERT INTO mergeMap (keepId, keepName, dropId, dropName) VALUES
  (59,  'Кетчуп томатный',            219, 'Кетчуп'),
  (220, 'Куриный бульон',             334, 'Куриный бульон '),
  (361, 'Фруктовое пюре',             363, 'Фруктовое пюре '),
  (231, 'Куриная грудка под шубой',   233, 'Куриная грудка под шубой '),
  (121, 'Салат с крабовыми палочками', 252, 'Салат с крабовыми палочками '),
  (121, 'Салат с крабовыми палочками', 272, 'Салат с крабовыми палочками и сыром'),
  (242, 'Крабовые палочки',           243, 'Палочки крабовые'),
  (373, 'Редис',                      372, 'Редиска'),
  (343, 'Буженина свиная',            342, 'Буженина'),
  (380, 'Котлеты свинина говядина',   379, 'Котлеты из говядины и свинины'),
  (366, 'Каша ячневая на воде',       441, 'Каша ячневая'),
  (148, 'Суп щи',                     245, 'Щи постные'),
  (364, 'Куриный суп с гречкой',      514, 'Суп куриный с гречкой'),
  (438, 'Плов с курицей',             459, 'Плов с цыплёнком'),
  (302, 'Творожная запеканка',        327, 'Запеканка из творога'),
  (476, 'Тушёная индейка с овощами',  475, 'Тушёная индейка с овощами 80'),
  (225, 'Кукурузная каша',             54, 'Каша кукурузная на молоке'),
  (224, 'Каша рисовая',                58, 'Рисовая каша на молоке'),
  (283, 'Морская капуста',            357, 'Морская капуста '),
  (75,  'Курица вареная тушёная',     502, 'Курица отварная'),
  (75,  'Курица вареная тушёная',     503, 'Курица тушёная');

CREATE TEMP TABLE assertions (
  name TEXT    NOT NULL,
  ok   INTEGER NOT NULL CHECK (ok = 1)
);

-- ---------------------------------------------------------------- pre-checks
-- Shown before the assertion fires, so a mismatch is visible in the output.
SELECT 'MISMATCH id/name', m.keepId, m.keepName, m.dropId, m.dropName
FROM mergeMap m
WHERE NOT (
  EXISTS (SELECT 1 FROM foodCatalogue WHERE id = m.keepId AND name = m.keepName)
  AND EXISTS (SELECT 1 FROM foodCatalogue WHERE id = m.dropId AND name = m.dropName)
);

INSERT INTO assertions VALUES
  ('pre: 21 pairs', (SELECT count(*) FROM mergeMap) = 21),
  ('pre: 19 distinct keeps', (SELECT count(DISTINCT keepId) FROM mergeMap) = 19),
  ('pre: every keep and drop exists with the exact expected name', NOT EXISTS (
    SELECT 1 FROM mergeMap m
    WHERE NOT (
      EXISTS (SELECT 1 FROM foodCatalogue WHERE id = m.keepId AND name = m.keepName)
      AND EXISTS (SELECT 1 FROM foodCatalogue WHERE id = m.dropId AND name = m.dropName)
    ))),
  ('pre: no chains (a keep is never a drop)', NOT EXISTS (SELECT 1 FROM mergeMap WHERE keepId IN (SELECT dropId FROM mergeMap))),
  ('pre: catalogue has 473 products', (SELECT count(*) FROM foodCatalogue) = 473),
  ('pre: no orphans in diary', NOT EXISTS (SELECT 1 FROM foodDiary WHERE foodCatalogueId NOT IN (SELECT id FROM foodCatalogue))),
  ('pre: no orphans in personal history', NOT EXISTS (SELECT 1 FROM foodPersonalKcalHistory WHERE foodCatalogueId NOT IN (SELECT id FROM foodCatalogue)));

-- ------------------------------------------------------------------ snapshots
CREATE TEMP TABLE diaryBefore AS
  SELECT id, usersId, dateISO, foodCatalogueId, foodWeight, history, ver, del FROM foodDiary;
CREATE TEMP TABLE historyBefore AS
  SELECT id, usersId, foodCatalogueId, yearMonth, kcalsPer100g, createdAt FROM foodPersonalKcalHistory;
CREATE TEMP TABLE catalogueBefore AS
  SELECT id, name, kcals, protein, fat, carbs, fiber, description, legacyName, nameVec, descriptionVec FROM foodCatalogue;

-- -------------------------------------------------------------------- changes
-- 1. Diary: only foodCatalogueId changes (ver is deliberately untouched, clients reload whole days).
UPDATE foodDiary
SET foodCatalogueId = (SELECT keepId FROM mergeMap WHERE dropId = foodDiary.foodCatalogueId)
WHERE foodCatalogueId IN (SELECT dropId FROM mergeMap);

-- 2. Personal kcal history, unique per (user, product, month). The keep's own month wins;
--    between several drops of one keep the lowest row id wins; the losers are removed first,
--    so the re-pointing below can never collide (a collision would abort the transaction).
DELETE FROM foodPersonalKcalHistory
WHERE id IN (
  SELECT h.id
  FROM foodPersonalKcalHistory h
  JOIN mergeMap m ON m.dropId = h.foodCatalogueId
  WHERE EXISTS (
          SELECT 1 FROM foodPersonalKcalHistory k
          WHERE k.usersId = h.usersId AND k.yearMonth = h.yearMonth AND k.foodCatalogueId = m.keepId)
     OR EXISTS (
          SELECT 1 FROM foodPersonalKcalHistory o
          JOIN mergeMap om ON om.dropId = o.foodCatalogueId
          WHERE om.keepId = m.keepId AND o.usersId = h.usersId AND o.yearMonth = h.yearMonth AND o.id < h.id)
);

UPDATE foodPersonalKcalHistory
SET foodCatalogueId = (SELECT keepId FROM mergeMap WHERE dropId = foodPersonalKcalHistory.foodCatalogueId)
WHERE foodCatalogueId IN (SELECT dropId FROM mergeMap);

-- 3. Catalogue: drop the merged-away products.
DELETE FROM foodCatalogue WHERE id IN (SELECT dropId FROM mergeMap);

-- ----------------------------------------------------------------- post-checks
INSERT INTO assertions VALUES
  ('post: diary row count unchanged', (SELECT count(*) FROM foodDiary) = (SELECT count(*) FROM diaryBefore)),
  ('post: every diary row identical except foodCatalogueId, which follows the map',
    (SELECT count(*)
     FROM diaryBefore b JOIN foodDiary a ON a.id = b.id
     WHERE a.usersId IS b.usersId AND a.dateISO IS b.dateISO AND a.foodWeight IS b.foodWeight
       AND a.history IS b.history AND a.ver IS b.ver AND a.del IS b.del
       AND a.foodCatalogueId IS COALESCE((SELECT keepId FROM mergeMap WHERE dropId = b.foodCatalogueId), b.foodCatalogueId))
    = (SELECT count(*) FROM diaryBefore)),
  ('post: moved diary rows = rows that pointed at drops',
    (SELECT count(*) FROM diaryBefore b JOIN foodDiary a ON a.id = b.id WHERE a.foodCatalogueId <> b.foodCatalogueId)
    = (SELECT count(*) FROM diaryBefore WHERE foodCatalogueId IN (SELECT dropId FROM mergeMap))),
  ('post: no diary row points at a drop', NOT EXISTS (SELECT 1 FROM foodDiary WHERE foodCatalogueId IN (SELECT dropId FROM mergeMap))),
  ('post: no personal history row points at a drop', NOT EXISTS (SELECT 1 FROM foodPersonalKcalHistory WHERE foodCatalogueId IN (SELECT dropId FROM mergeMap))),
  ('post: personal history = one row per (user, mapped product, month)',
    (SELECT count(*) FROM foodPersonalKcalHistory)
    = (SELECT count(*) FROM (
         SELECT DISTINCT usersId, COALESCE((SELECT keepId FROM mergeMap WHERE dropId = foodCatalogueId), foodCatalogueId), yearMonth
         FROM historyBefore))),
  ('post: every surviving history row existed before, values intact',
    (SELECT count(*) FROM foodPersonalKcalHistory a JOIN historyBefore b ON b.id = a.id
     WHERE a.usersId = b.usersId AND a.yearMonth = b.yearMonth AND a.kcalsPer100g IS b.kcalsPer100g AND a.createdAt IS b.createdAt
       AND a.foodCatalogueId = COALESCE((SELECT keepId FROM mergeMap WHERE dropId = b.foodCatalogueId), b.foodCatalogueId))
    = (SELECT count(*) FROM foodPersonalKcalHistory)),
  ('post: history rows of the keeps themselves are untouched',
    (SELECT count(*) FROM historyBefore b JOIN foodPersonalKcalHistory a ON a.id = b.id
     WHERE b.foodCatalogueId IN (SELECT keepId FROM mergeMap) AND a.foodCatalogueId = b.foodCatalogueId)
    = (SELECT count(*) FROM historyBefore WHERE foodCatalogueId IN (SELECT keepId FROM mergeMap))),
  ('post: catalogue = 473 - 21', (SELECT count(*) FROM foodCatalogue) = 452),
  ('post: no drop left in catalogue', NOT EXISTS (SELECT 1 FROM foodCatalogue WHERE id IN (SELECT dropId FROM mergeMap))),
  ('post: all keeps still present', (SELECT count(*) FROM foodCatalogue WHERE id IN (SELECT keepId FROM mergeMap)) = 19),
  ('post: every other catalogue row byte-identical (incl. embeddings)',
    (SELECT count(*) FROM foodCatalogue a JOIN catalogueBefore b ON b.id = a.id
     WHERE a.name IS b.name AND a.kcals IS b.kcals AND a.protein IS b.protein AND a.fat IS b.fat AND a.carbs IS b.carbs
       AND a.fiber IS b.fiber AND a.description IS b.description AND a.legacyName IS b.legacyName
       AND a.nameVec IS b.nameVec AND a.descriptionVec IS b.descriptionVec)
    = 452),
  ('post: no orphans in diary', NOT EXISTS (SELECT 1 FROM foodDiary WHERE foodCatalogueId NOT IN (SELECT id FROM foodCatalogue))),
  ('post: no orphans in personal history', NOT EXISTS (SELECT 1 FROM foodPersonalKcalHistory WHERE foodCatalogueId NOT IN (SELECT id FROM foodCatalogue)));

SELECT 'ALL ASSERTIONS PASSED: ' || count(*) FROM assertions;
