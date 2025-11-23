DROP TABLE IF EXISTS test_vacuum;
CREATE TABLE test_vacuum (
    id SERIAL PRIMARY KEY,
    valeur TEXT
) WITH (autovacuum_enabled = false, fillfactor=100);  -- Pas de TOAST ici, fillfactor max

-- ➕ Ajoutons suffisamment de lignes pour forcer plusieurs pages (environ 100 lignes par page ici)
INSERT INTO test_vacuum (valeur)
SELECT repeat('x', 100) FROM generate_series(1, 10000);

-- 🧹 Supprimons les 5000 premières lignes
DELETE FROM test_vacuum WHERE id <= 5000;

-- 📏 Vérifions la taille logique
SELECT relname,
       relpages,
       pg_size_pretty(pg_relation_size(relid)) AS table_size
FROM pg_catalog.pg_statio_user_tables
WHERE relname = 'test_vacuum';

-- 📋 Contrôle rapide : combien de lignes par page ?
SELECT ctid, id FROM test_vacuum ORDER BY ctid LIMIT 100;

-- 🧠 Générons une plage de CTID à mettre à jour
-- 👇 Ici on cible la moitié inférieure des CTID restants (dans les blocs 50 à 100 par ex.)
DO $$
DECLARE
    from_block int := 50;
    to_block   int := 100;
    max_tuples int := 200; -- estimation grossière de lignes par page
    r tid;
BEGIN
    FOR r IN
        SELECT format('(%s,%s)', b, o)::tid
        FROM generate_series(from_block, to_block) AS b,
             generate_series(1, max_tuples) AS o
    LOOP
        UPDATE test_vacuum
        SET valeur = valeur || ''
        WHERE ctid = r;
    END LOOP;
END $$;

-- 🚀 VACUUM pour observer l'effet du nettoyage
VACUUM test_vacuum;

-- 📏 Vérifions de nouveau la taille logique
SELECT relname,
       relpages,
       pg_size_pretty(pg_relation_size(relid)) AS table_size
FROM pg_catalog.pg_statio_user_tables
WHERE relname = 'test_vacuum';
