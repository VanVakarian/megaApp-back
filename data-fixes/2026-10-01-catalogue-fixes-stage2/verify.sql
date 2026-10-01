-- Independent post-fix verification. Does not reuse anything from fix.sql: every expected value is
-- typed again here and everything is compared against the pre-change copy.
-- Expects: main = database AFTER the fix, `b` = attached database BEFORE it. Read-only.

CREATE TEMP TABLE checks (name TEXT NOT NULL, ok INTEGER NOT NULL CHECK (ok = 1));

-- human-readable evidence: the 4 products now
SELECT 'NOW', id, name, kcals, protein, fat, carbs, fiber, description FROM main.foodCatalogue WHERE id IN (188, 223, 420, 470) ORDER BY id;

INSERT INTO checks VALUES
  ('catalogue: same id set, 452 products',
    (SELECT count(*) FROM main.foodCatalogue) = 452
    AND NOT EXISTS (SELECT id FROM b.foodCatalogue EXCEPT SELECT id FROM main.foodCatalogue)
    AND NOT EXISTS (SELECT id FROM main.foodCatalogue EXCEPT SELECT id FROM b.foodCatalogue)),
  ('catalogue: exactly products 188, 223, 420, 470 differ from before (all columns)',
    (SELECT group_concat(id) FROM (
       SELECT id FROM (
         SELECT id, name, kcals, protein, fat, carbs, fiber, description, legacyName, nameVec, descriptionVec FROM main.foodCatalogue
         EXCEPT
         SELECT id, name, kcals, protein, fat, carbs, fiber, description, legacyName, nameVec, descriptionVec FROM b.foodCatalogue)
       ORDER BY id)) = '188,223,420,470'),
  ('catalogue: embeddings and legacyName of ALL products identical to before',
    NOT EXISTS (
      SELECT id, legacyName, nameVec, descriptionVec FROM b.foodCatalogue
      EXCEPT
      SELECT id, legacyName, nameVec, descriptionVec FROM main.foodCatalogue)),
  ('188: name kept, 100 kcal, macros and fiber zero, new description',
    EXISTS (SELECT 1 FROM main.foodCatalogue WHERE id = 188 AND name = 'kcals1' AND kcals = 100
            AND protein = 0 AND fat = 0 AND carbs = 0 AND fiber = 0
            AND description = 'Технический продукт для добивки дня: 1 г = 1 ккал. Не используется.')),
  ('223: only carbs changed (72.8 -> 17.0)',
    EXISTS (SELECT 1 FROM main.foodCatalogue m JOIN b.foodCatalogue o ON o.id = m.id
            WHERE m.id = 223 AND o.carbs = 72.8 AND m.carbs = 17.0
              AND m.name = o.name AND m.kcals = o.kcals AND m.protein = o.protein AND m.fat = o.fat
              AND m.fiber = o.fiber AND m.description = o.description)),
  ('470: kcals 396 -> 101, macros and name kept, Chinese word gone',
    EXISTS (SELECT 1 FROM main.foodCatalogue m JOIN b.foodCatalogue o ON o.id = m.id
            WHERE m.id = 470 AND o.kcals = 396 AND m.kcals = 101
              AND m.name = o.name AND m.protein = o.protein AND m.fat = o.fat AND m.carbs = o.carbs AND m.fiber = o.fiber
              AND instr(o.description, '调味料') > 0 AND instr(m.description, '调味料') = 0
              AND m.description = replace(o.description, 'как调味料', 'как приправа'))),
  ('420: renamed to the salad, kcals and macros kept, description about the salad',
    EXISTS (SELECT 1 FROM main.foodCatalogue m JOIN b.foodCatalogue o ON o.id = m.id
            WHERE m.id = 420 AND o.name = 'Секла майонез' AND m.name = 'Салат из свеклы с майонезом'
              AND m.kcals = o.kcals AND m.protein = o.protein AND m.fat = o.fat AND m.carbs = o.carbs AND m.fiber = o.fiber
              AND m.description = 'Домашний салат из варёной свёклы, заправленный майонезом. Основная часть калорий приходится на майонез.')),
  ('catalogue: names still unique', (SELECT count(DISTINCT name) FROM main.foodCatalogue) = (SELECT count(*) FROM main.foodCatalogue)),
  ('diary: identical to before (same ids, all columns)',
    (SELECT count(*) FROM b.foodDiary) = (SELECT count(*) FROM main.foodDiary)
    AND NOT EXISTS (
      SELECT id, usersId, dateISO, foodCatalogueId, foodWeight, history, ver, del FROM b.foodDiary
      EXCEPT
      SELECT id, usersId, dateISO, foodCatalogueId, foodWeight, history, ver, del FROM main.foodDiary)),
  ('history: 470 had 3 rows before, 0 now', (SELECT count(*) FROM b.foodPersonalKcalHistory WHERE foodCatalogueId = 470) = 3
    AND (SELECT count(*) FROM main.foodPersonalKcalHistory WHERE foodCatalogueId = 470) = 0),
  ('history: all other rows identical, nothing added',
    (SELECT count(*) FROM main.foodPersonalKcalHistory) = (SELECT count(*) FROM b.foodPersonalKcalHistory) - 3
    AND NOT EXISTS (
      SELECT id, usersId, foodCatalogueId, yearMonth, kcalsPer100g, createdAt FROM b.foodPersonalKcalHistory WHERE foodCatalogueId <> 470
      EXCEPT
      SELECT id, usersId, foodCatalogueId, yearMonth, kcalsPer100g, createdAt FROM main.foodPersonalKcalHistory)
    AND NOT EXISTS (
      SELECT id, usersId, foodCatalogueId, yearMonth, kcalsPer100g, createdAt FROM main.foodPersonalKcalHistory
      EXCEPT
      SELECT id, usersId, foodCatalogueId, yearMonth, kcalsPer100g, createdAt FROM b.foodPersonalKcalHistory)),
  ('no orphans: diary and personal history', NOT EXISTS (SELECT 1 FROM main.foodDiary WHERE foodCatalogueId NOT IN (SELECT id FROM main.foodCatalogue))
    AND NOT EXISTS (SELECT 1 FROM main.foodPersonalKcalHistory WHERE foodCatalogueId NOT IN (SELECT id FROM main.foodCatalogue))),
  ('other tables untouched: users, userSettings, foodBodyWeight, syncOperations',
    (SELECT count(*) FROM b.users) = (SELECT count(*) FROM main.users)
    AND (SELECT count(*) FROM b.userSettings) = (SELECT count(*) FROM main.userSettings)
    AND (SELECT count(*) FROM b.foodBodyWeight) = (SELECT count(*) FROM main.foodBodyWeight)
    AND (SELECT count(*) FROM b.syncOperations) = (SELECT count(*) FROM main.syncOperations));

SELECT 'VERIFY OK: ' || count(*) || ' checks' FROM checks;
SELECT 'counts', 'catalogue ' || (SELECT count(*) FROM main.foodCatalogue),
       'diary ' || (SELECT count(*) FROM main.foodDiary),
       'history ' || (SELECT count(*) FROM main.foodPersonalKcalHistory) || ' (was ' || (SELECT count(*) FROM b.foodPersonalKcalHistory) || ')';
