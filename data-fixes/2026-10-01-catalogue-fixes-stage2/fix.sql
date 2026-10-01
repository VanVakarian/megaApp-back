-- Catalogue cleanup, stage 2 (plans/37): placeholder description and data errors in 4 products.
-- Body only: the caller owns BEGIN / COMMIT / ROLLBACK (see apply.sql, rehearse.sql).
-- Every assertion inserts into a CHECK-guarded table: a violated assertion raises an error,
-- the sqlite3 shell (.bail on) stops, and the open transaction is never committed.
-- Embeddings (nameVec, descriptionVec) are deliberately untouched: plans/37 stage 7.

CREATE TEMP TABLE assertions (
  name TEXT    NOT NULL,
  ok   INTEGER NOT NULL CHECK (ok = 1)
);

-- One row per product: the exact expected state before and the exact wanted state after.
CREATE TEMP TABLE fixes (
  id             INTEGER NOT NULL PRIMARY KEY,
  oldName        TEXT    NOT NULL, oldKcals INTEGER NOT NULL, oldProtein REAL NOT NULL, oldFat REAL NOT NULL,
  oldCarbs       REAL    NOT NULL, oldFiber REAL NOT NULL,    oldDescription TEXT NOT NULL,
  newName        TEXT    NOT NULL, newKcals INTEGER NOT NULL, newProtein REAL NOT NULL, newFat REAL NOT NULL,
  newCarbs       REAL    NOT NULL, newFiber REAL NOT NULL,    newDescription TEXT NOT NULL
);

INSERT INTO fixes VALUES
  -- technical "fill the day" product: only the description (wrongly about chicken) and the chicken macros change
  (188,
   'kcals1', 100, 22.5, 1.5, 0.0, 0.0,
   'Куриное филе без кожи, запеченное или отварное. Постное мясное блюдо с высоким содержанием белка.',
   'kcals1', 100, 0.0, 0.0, 0.0, 0.0,
   'Технический продукт для добивки дня: 1 г = 1 ккал. Не используется.'),
  -- cooked porridge: carbs of the dry grain (72.8) -> value matching 98 kcal
  (223,
   'Пшённая каша (молоко/вода)', 98, 4.2, 1.5, 72.8, 1.9,
   'Каша из пшена, сваренная на воде. Зерновое блюдо без добавления сахара и масла.',
   'Пшённая каша (молоко/вода)', 98, 4.2, 1.5, 17.0, 1.9,
   'Каша из пшена, сваренная на воде. Зерновое блюдо без добавления сахара и масла.'),
  -- concentrated soy sauce: kcal follows the macros (7*4 + 1*9 + 16*4 = 101); stray Chinese word in the description
  (470,
   'Соевый соус концентрированный', 396, 7.0, 1.0, 16.0, 5.0,
   'Концентрированный соевый соус — это густая ароматная приправа на основе соевых бобов, пшеницы и соли. Используется как调味料 в азиатской кухне, относится к категории соусов.',
   'Соевый соус концентрированный', 101, 7.0, 1.0, 16.0, 5.0,
   'Концентрированный соевый соус — это густая ароматная приправа на основе соевых бобов, пшеницы и соли. Используется как приправа в азиатской кухне, относится к категории соусов.'),
  -- homemade beet-and-mayonnaise salad, not plain mayonnaise ("Секла" = typo of "Свекла")
  (420,
   'Секла майонез', 600, 1.0, 65.0, 2.0, 0.0,
   'Майонез на основе растительных масел с добавлением яичного желтка и уксуса. Жирный и калорийный соус, используется как заправка для салатов и холодных закусок.',
   'Салат из свеклы с майонезом', 600, 1.0, 65.0, 2.0, 0.0,
   'Домашний салат из варёной свёклы, заправленный майонезом. Основная часть калорий приходится на майонез.');

-- ---------------------------------------------------------------- pre-checks
-- Shown before the assertion fires, so a mismatch is visible in the output.
SELECT 'MISMATCH before', f.id
FROM fixes f
WHERE NOT EXISTS (
  SELECT 1 FROM foodCatalogue c
  WHERE c.id = f.id AND c.name IS f.oldName AND c.kcals IS f.oldKcals AND c.protein IS f.oldProtein
    AND c.fat IS f.oldFat AND c.carbs IS f.oldCarbs AND c.fiber IS f.oldFiber AND c.description IS f.oldDescription);

INSERT INTO assertions VALUES
  ('pre: 4 fixes', (SELECT count(*) FROM fixes) = 4),
  ('pre: every product is in exactly the expected old state', NOT EXISTS (
    SELECT 1 FROM fixes f
    WHERE NOT EXISTS (
      SELECT 1 FROM foodCatalogue c
      WHERE c.id = f.id AND c.name IS f.oldName AND c.kcals IS f.oldKcals AND c.protein IS f.oldProtein
        AND c.fat IS f.oldFat AND c.carbs IS f.oldCarbs AND c.fiber IS f.oldFiber AND c.description IS f.oldDescription))),
  ('pre: catalogue has 452 products', (SELECT count(*) FROM foodCatalogue) = 452),
  ('pre: new name of 420 is free', NOT EXISTS (SELECT 1 FROM foodCatalogue WHERE name = 'Салат из свеклы с майонезом')),
  ('pre: product 470 has exactly 3 personal history rows', (SELECT count(*) FROM foodPersonalKcalHistory WHERE foodCatalogueId = 470) = 3);

-- ------------------------------------------------------------------ snapshots
CREATE TEMP TABLE catalogueBefore AS
  SELECT id, name, kcals, protein, fat, carbs, fiber, description, legacyName, nameVec, descriptionVec FROM foodCatalogue;
CREATE TEMP TABLE diaryBefore AS
  SELECT id, usersId, dateISO, foodCatalogueId, foodWeight, history, ver, del FROM foodDiary;
CREATE TEMP TABLE historyBefore AS
  SELECT id, usersId, foodCatalogueId, yearMonth, kcalsPer100g, createdAt FROM foodPersonalKcalHistory;

-- -------------------------------------------------------------------- changes
-- 1. Catalogue: only name / kcals / macros / description, and only for the 4 products.
UPDATE foodCatalogue
SET name        = (SELECT newName        FROM fixes WHERE id = foodCatalogue.id),
    kcals       = (SELECT newKcals       FROM fixes WHERE id = foodCatalogue.id),
    protein     = (SELECT newProtein     FROM fixes WHERE id = foodCatalogue.id),
    fat         = (SELECT newFat         FROM fixes WHERE id = foodCatalogue.id),
    carbs       = (SELECT newCarbs       FROM fixes WHERE id = foodCatalogue.id),
    fiber       = (SELECT newFiber       FROM fixes WHERE id = foodCatalogue.id),
    description = (SELECT newDescription FROM fixes WHERE id = foodCatalogue.id)
WHERE id IN (SELECT id FROM fixes);

-- 2. Personal history of 470: the 3 rows were fitted from the wrong 396 kcal and would keep the
--    diary entries at ~380 kcal/100 g; without rows the entries fall back to the catalogue value.
DELETE FROM foodPersonalKcalHistory WHERE foodCatalogueId = 470;

-- ----------------------------------------------------------------- post-checks
INSERT INTO assertions VALUES
  ('post: every fixed product has exactly the new values', NOT EXISTS (
    SELECT 1 FROM fixes f
    WHERE NOT EXISTS (
      SELECT 1 FROM foodCatalogue c
      WHERE c.id = f.id AND c.name IS f.newName AND c.kcals IS f.newKcals AND c.protein IS f.newProtein
        AND c.fat IS f.newFat AND c.carbs IS f.newCarbs AND c.fiber IS f.newFiber AND c.description IS f.newDescription))),
  ('post: fixed products keep legacyName and both embeddings byte-identical',
    (SELECT count(*) FROM foodCatalogue a JOIN catalogueBefore b ON b.id = a.id
     WHERE a.id IN (SELECT id FROM fixes) AND a.legacyName IS b.legacyName AND a.nameVec IS b.nameVec AND a.descriptionVec IS b.descriptionVec)
    = 4),
  ('post: every other catalogue row byte-identical (incl. embeddings)',
    (SELECT count(*) FROM foodCatalogue a JOIN catalogueBefore b ON b.id = a.id
     WHERE a.id NOT IN (SELECT id FROM fixes)
       AND a.name IS b.name AND a.kcals IS b.kcals AND a.protein IS b.protein AND a.fat IS b.fat AND a.carbs IS b.carbs
       AND a.fiber IS b.fiber AND a.description IS b.description AND a.legacyName IS b.legacyName
       AND a.nameVec IS b.nameVec AND a.descriptionVec IS b.descriptionVec)
    = 448),
  ('post: catalogue still has 452 products', (SELECT count(*) FROM foodCatalogue) = 452),
  ('post: diary untouched (same rows, all columns)',
    (SELECT count(*) FROM foodDiary) = (SELECT count(*) FROM diaryBefore)
    AND NOT EXISTS (
      SELECT id, usersId, dateISO, foodCatalogueId, foodWeight, history, ver, del FROM diaryBefore
      EXCEPT
      SELECT id, usersId, dateISO, foodCatalogueId, foodWeight, history, ver, del FROM foodDiary)),
  ('post: personal history = before minus the 3 rows of 470',
    (SELECT count(*) FROM foodPersonalKcalHistory) = (SELECT count(*) FROM historyBefore) - 3
    AND NOT EXISTS (SELECT 1 FROM foodPersonalKcalHistory WHERE foodCatalogueId = 470)
    AND NOT EXISTS (
      SELECT id, usersId, foodCatalogueId, yearMonth, kcalsPer100g, createdAt FROM historyBefore WHERE foodCatalogueId <> 470
      EXCEPT
      SELECT id, usersId, foodCatalogueId, yearMonth, kcalsPer100g, createdAt FROM foodPersonalKcalHistory)),
  ('post: no orphans in diary', NOT EXISTS (SELECT 1 FROM foodDiary WHERE foodCatalogueId NOT IN (SELECT id FROM foodCatalogue))),
  ('post: no orphans in personal history', NOT EXISTS (SELECT 1 FROM foodPersonalKcalHistory WHERE foodCatalogueId NOT IN (SELECT id FROM foodCatalogue)));

SELECT 'ALL ASSERTIONS PASSED: ' || count(*) FROM assertions;
