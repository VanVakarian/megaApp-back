-- A category's chart color is hue + lightness + saturation; NULL lightness/saturation follow the user's palette default.
ALTER TABLE moneyCategories ADD COLUMN lightness INTEGER CHECK(lightness IS NULL OR lightness BETWEEN 0 AND 100);
ALTER TABLE moneyCategories ADD COLUMN saturation INTEGER CHECK(saturation IS NULL OR saturation BETWEEN 0 AND 100);
