-- Plan 37 (stage 7 prerequisite): reset embeddings of texts changed by stage 2 so they are
-- regenerated later from the CURRENT text (NULL = "needs generation", see the lab fill-missing call).
--   188 "kcals1":                     description changed          -> descriptionVec
--   420 "Салат из свеклы с майонезом": name + description changed   -> nameVec, descriptionVec
--   470 "Соевый соус концентрированный": description changed        -> descriptionVec
-- Stage 1 merges changed no name or description, stage 2 changed no other text.
-- Body only: the caller owns BEGIN / COMMIT. A violated assertion aborts the transaction (.bail on).

CREATE TEMP TABLE assertions (
  name TEXT    NOT NULL,
  ok   INTEGER NOT NULL CHECK (ok = 1)
);

INSERT INTO assertions VALUES
  ('pre: catalogue has 452 products', (SELECT count(*) FROM foodCatalogue) = 452),
  ('pre: the 3 products exist with the stage 2 names', (SELECT count(*) FROM foodCatalogue WHERE
      (id = 188 AND name = 'kcals1')
   OR (id = 420 AND name = 'Салат из свеклы с майонезом')
   OR (id = 470 AND name = 'Соевый соус концентрированный')) = 3),
  ('pre: the 4 vectors to reset are currently set', (SELECT count(*) FROM foodCatalogue WHERE
      (id = 188 AND descriptionVec IS NOT NULL)
   OR (id = 420 AND nameVec IS NOT NULL AND descriptionVec IS NOT NULL)
   OR (id = 470 AND descriptionVec IS NOT NULL)) = 3);

CREATE TEMP TABLE catalogueBefore AS
  SELECT id, name, kcals, protein, fat, carbs, fiber, description, legacyName, nameVec, descriptionVec FROM foodCatalogue;
CREATE TEMP TABLE countsBefore AS
  SELECT (SELECT count(*) FROM foodDiary) AS diary, (SELECT count(*) FROM foodPersonalKcalHistory) AS history;

UPDATE foodCatalogue SET descriptionVec = NULL WHERE id IN (188, 470);
UPDATE foodCatalogue SET nameVec = NULL, descriptionVec = NULL WHERE id = 420;

INSERT INTO assertions VALUES
  ('post: exactly the 4 intended vectors are NULL', (SELECT count(*) FROM foodCatalogue WHERE nameVec IS NULL OR descriptionVec IS NULL) = 3
    AND (SELECT descriptionVec IS NULL FROM foodCatalogue WHERE id = 188)
    AND (SELECT nameVec IS NOT NULL FROM foodCatalogue WHERE id = 188)
    AND (SELECT nameVec IS NULL AND descriptionVec IS NULL FROM foodCatalogue WHERE id = 420)
    AND (SELECT descriptionVec IS NULL FROM foodCatalogue WHERE id = 470)
    AND (SELECT nameVec IS NOT NULL FROM foodCatalogue WHERE id = 470)),
  ('post: every text/number column of every product identical', (SELECT count(*) FROM foodCatalogue a JOIN catalogueBefore b ON b.id = a.id
     WHERE a.name IS b.name AND a.kcals IS b.kcals AND a.protein IS b.protein AND a.fat IS b.fat AND a.carbs IS b.carbs
       AND a.fiber IS b.fiber AND a.description IS b.description AND a.legacyName IS b.legacyName) = 452),
  ('post: all other vectors byte-identical', (SELECT count(*) FROM foodCatalogue a JOIN catalogueBefore b ON b.id = a.id
     WHERE (a.id = 188 OR a.id = 470 OR a.id = 420 OR (a.nameVec IS b.nameVec AND a.descriptionVec IS b.descriptionVec))
       AND (a.id <> 188 OR a.nameVec IS b.nameVec)
       AND (a.id <> 470 OR a.nameVec IS b.nameVec)) = 452),
  ('post: untouched vectors of the 3 products stay byte-identical', (SELECT count(*) FROM foodCatalogue a JOIN catalogueBefore b ON b.id = a.id
     WHERE (a.id = 188 AND a.nameVec IS b.nameVec) OR (a.id = 470 AND a.nameVec IS b.nameVec)) = 2),
  ('post: diary and personal history untouched', (SELECT diary FROM countsBefore) = (SELECT count(*) FROM foodDiary)
    AND (SELECT history FROM countsBefore) = (SELECT count(*) FROM foodPersonalKcalHistory));

SELECT 'ALL ASSERTIONS PASSED: ' || count(*) FROM assertions;
SELECT 'NULL vectors now', id, name, nameVec IS NULL AS nameVecNull, descriptionVec IS NULL AS descriptionVecNull
FROM foodCatalogue WHERE nameVec IS NULL OR descriptionVec IS NULL ORDER BY id;
