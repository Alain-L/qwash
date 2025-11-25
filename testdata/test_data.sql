-- Without TOAST
DO $$
DECLARE
  bloat_levels text[] := ARRAY['low', 'medium', 'high'];
  delete_percents int[] := ARRAY[10, 30, 70];
  
  multipliers int[] := ARRAY[10, 100, 300];  -- or ARRAY[10, 50, 300] or ARRAY[10, 20, 50, 200]
  
  base_count int := 1000;
  mult int;
  mult_suffix text;
  table_name text;
  version_suffix text;
  fillfactor_clause text;
  i int;
  j int;
  v int;
BEGIN
  FOR i IN 1 .. array_length(bloat_levels, 1) LOOP
    FOR j IN 1 .. array_length(multipliers, 1) LOOP
      mult := multipliers[j];
      mult_suffix := '_' || mult || 'x';

      FOR v IN 1..2 LOOP
        IF v = 1 THEN
           version_suffix := '';
           fillfactor_clause := '';
        ELSE
           version_suffix := '_fill80';
           fillfactor_clause := 'fillfactor = 80, ';
        END IF;

        table_name := bloat_levels[i] || '_bloat_table' || mult_suffix || version_suffix;
        EXECUTE 'DROP TABLE IF EXISTS ' || quote_ident(table_name) || ';';

        EXECUTE 'CREATE TABLE ' || quote_ident(table_name) || ' (
            id SERIAL PRIMARY KEY,
            col1 INTEGER,
            col2 DOUBLE PRECISION,
            col3 VARCHAR(20),
            col4 VARCHAR(40),
            col5 SMALLINT,
            col6 NUMERIC(10,2),
            col7 CHAR(10),
            col8 VARCHAR(50)
        ) WITH (' || fillfactor_clause || 'autovacuum_enabled = false);';

        EXECUTE 'INSERT INTO ' || quote_ident(table_name) || ' (col1, col2, col3, col4, col5, col6, col7, col8)
                 SELECT 
                   g,
                   g * random(),
                   ''text_'' || g,
                   ''sample_data_'' || g,
                   (g % 32767)::smallint,
                   round(g * 1.23, 2),
                   LPAD(g::text, 10, ''0''),
                   ''data_'' || g
                 FROM generate_series(1, ' 
                 || ((base_count * mult) + ((random() * 100)::int)) 
                 || ') g;';

        EXECUTE 'DELETE FROM ' || quote_ident(table_name) || 
                ' WHERE id % 100 < ' 
                || (delete_percents[i] + ((random() * 5 - 2)::int)) || ';';
      END LOOP;
    END LOOP;
  END LOOP;
END
$$;
ANALYZE;

-- Without TOAST to compare with pg_compacttable and VACUUM FULL
DO $$
DECLARE
  bloat_levels text[] := ARRAY['low', 'medium', 'high'];
  delete_percents int[] := ARRAY[10, 30, 70];
  table_prefixes text[] := ARRAY['vacuumfull_', 'pgcompacttable_', 'qwash_', 'bloated_'];

  base_count int := 1000;
  mult int := 300;
  base_rows int;
  del_percent int;

  table_name text;
  full_table_name text;

  i int;
  p int;
BEGIN
  FOR i IN 1 .. array_length(bloat_levels, 1) LOOP
    del_percent := delete_percents[i];
    table_name := bloat_levels[i] || '_bloat_table_300x';
    base_rows := base_count * mult;

    FOR p IN 1 .. array_length(table_prefixes, 1) LOOP
      full_table_name := table_prefixes[p] || table_name;

      EXECUTE format('DROP TABLE IF EXISTS %I;', full_table_name);

      EXECUTE format(
        'CREATE TABLE %I (
          id SERIAL PRIMARY KEY,
          col1 INTEGER,
          col2 DOUBLE PRECISION,
          col3 VARCHAR(20),
          col4 VARCHAR(40),
          col5 SMALLINT,
          col6 NUMERIC(10,2),
          col7 CHAR(10),
          col8 VARCHAR(50)
        ) WITH (autovacuum_enabled = false);', full_table_name
      );
      -- pour plus tard : fillfactor = 80

      EXECUTE format(
        'INSERT INTO %I (col1, col2, col3, col4, col5, col6, col7, col8)
         SELECT 
           g,
           g * random(),
           ''text_'' || g,
           ''sample_data_'' || g,
           (g %% 32767)::smallint,
           round(g * 1.23, 2),
           LPAD(g::text, 10, ''0''),
           ''data_'' || g
         FROM generate_series(1, %s) g;', full_table_name, base_rows
      );

      EXECUTE format(
        'DELETE FROM %I WHERE id %% 100 < %s;', full_table_name, del_percent
      );

    END LOOP;
  END LOOP;
END
$$;



-- With TOAST
DO $$
DECLARE
  -- Paramètres simplifiés pour réduire le nombre total de tables créées
  bloat_levels text[] := ARRAY['low', 'medium', 'high'];
  delete_percents int[] := ARRAY[10, 30, 70];  -- Pourcentage de suppression de base par niveau de bloat
  multipliers text[] := ARRAY['_10x', '_100x', '_1000x'];
  mode_types text[] := ARRAY['normal', 'toast'];  -- normal : table classique, toast : colonnes toastable
  
  base_count int := 1000;  -- Nombre de lignes de base
  mult int;              -- Valeur multiplicative (10, 100 ou 1000)
  
  table_name text;       -- Nom de la table à créer
  version_suffix text;   -- Suffixe indiquant la variante de fillfactor
  fillfactor_clause text;-- Clause pour définir le fillfactor (si nécessaire)
  
  mode text;             -- 'normal' ou 'toast'
  i int;
  j int;
  v int;
BEGIN
  -- Boucle sur les niveaux de bloat
  FOR i IN 1 .. array_length(bloat_levels, 1) LOOP
    -- Boucle sur les multiplicateurs (_10x, _100x, _1000x)
    FOR j IN 1 .. array_length(multipliers, 1) LOOP
      IF j = 1 THEN
         mult := 10;
      ELSIF j = 2 THEN
         mult := 100;
      ELSIF j = 3 THEN
         mult := 1000;
      END IF;
      
      -- Boucle sur les deux modes : normal et toast
      FOR mode IN SELECT unnest(mode_types) LOOP
      
        -- Pour chaque combinaison, créer deux versions : fillfactor par défaut (100) et fillfactor = 80
        FOR v IN 1..2 LOOP
          IF v = 1 THEN
             version_suffix := '';           -- Version par défaut (fillfactor 100)
             fillfactor_clause := '';
          ELSE
             version_suffix := '_fill80';      -- Version avec fillfactor = 80
             fillfactor_clause := 'fillfactor = 80, ';
          END IF;
          
          -- Construction du nom de table : préfixe 'toast_' si mode = 'toast'
          IF mode = 'normal' THEN
            table_name := bloat_levels[i] || '_bloat_table' || multipliers[j] || version_suffix;
          ELSE
            table_name := 'toast_' || bloat_levels[i] || '_bloat_table' || multipliers[j] || version_suffix;
          END IF;
          
          -- Suppression de la table si elle existe
          EXECUTE 'DROP TABLE IF EXISTS ' || quote_ident(table_name) || ';';
          
          -- Création de la table en fonction du mode
          IF mode = 'normal' THEN
            -- Table classique : types standards, moins sujette à TOAST
            EXECUTE 'CREATE TABLE ' || quote_ident(table_name) || ' (
                id SERIAL PRIMARY KEY,
                col1 INTEGER,
                col2 DOUBLE PRECISION,
                col3 VARCHAR(20),
                col4 VARCHAR(40),
                col5 SMALLINT,
                col6 NUMERIC(10,2),
                col7 CHAR(10),
                col8 VARCHAR(50)
            ) WITH (' || fillfactor_clause || 'autovacuum_enabled = false);';
            
            -- Insertion de lignes avec des données variées.
            -- Le nombre total de lignes est : base_count * mult plus un bruit aléatoire entre 0 et 99.
            EXECUTE 'INSERT INTO ' || quote_ident(table_name) || ' (col1, col2, col3, col4, col5, col6, col7, col8)
                     SELECT 
                       g,
                       g * random(),
                       ''text_'' || g,
                       ''sample_data_'' || g,
                       (g % 32767)::smallint,
                       round(g * 1.23, 2),
                       LPAD(g::text, 10, ''0''),
                       ''data_'' || g
                     FROM generate_series(1, ' 
                     || ((base_count * mult) + ((random() * 100)::int)) 
                     || ') g;';
          
          ELSE
            -- Table toast : colonnes toastables avec types volumineux
            EXECUTE 'CREATE TABLE ' || quote_ident(table_name) || ' (
                id SERIAL PRIMARY KEY,
                col1 INTEGER,
                col2 DOUBLE PRECISION,
                col3 TEXT,         -- Texte long
                col4 BYTEA,        -- Données binaires
                col5 SMALLINT,
                col6 NUMERIC(10,2),
                col7 CHAR(10),
                col8 JSONB         -- Données JSON
            ) WITH (' || fillfactor_clause || 'autovacuum_enabled = false);';
            
            -- Désactivation de la compression pour forcer le stockage externe (TOAST)
            EXECUTE 'ALTER TABLE ' || quote_ident(table_name) || ' ALTER COLUMN col3 SET STORAGE EXTERNAL;';
            EXECUTE 'ALTER TABLE ' || quote_ident(table_name) || ' ALTER COLUMN col4 SET STORAGE EXTERNAL;';
            EXECUTE 'ALTER TABLE ' || quote_ident(table_name) || ' ALTER COLUMN col8 SET STORAGE EXTERNAL;';
            
            -- Insertion de lignes avec de grandes données; bruit aléatoire ajouté au nombre d'insertion
            EXECUTE 'INSERT INTO ' || quote_ident(table_name) || ' (col1, col2, col3, col4, col5, col6, col7, col8)
                     SELECT 
                       g,
                       g * random(),
                       repeat(''large_text_'', 100) || g,         -- col3: très long texte
                       decode(repeat(''AB'', 100), ''hex''),        -- col4: données binaires
                       (g % 32767)::smallint,
                       round(g * 1.23, 2),
                       LPAD(g::text, 10, ''0''),
                       jsonb_build_object(''g'', g, ''text'', repeat(''json_text_'', 100))
                     FROM generate_series(1, ' 
                     || ((base_count * mult) + ((random() * 100)::int)) 
                     || ') g;';
          END IF;
          
          -- Suppression d'un pourcentage contrôlé de lignes pour simuler le bloat.
          -- Un bruit aléatoire (entre -2 et +3) est ajouté au pourcentage de base.
          EXECUTE 'DELETE FROM ' || quote_ident(table_name) || 
                  ' WHERE id % 100 < ' 
                  || (delete_percents[i] + ((random() * 5 - 2)::int)) || ';';
        END LOOP;  -- Fin boucle fillfactor
      END LOOP;  -- Fin boucle mode
    END LOOP;  -- Fin boucle multipliers
  END LOOP;  -- Fin boucle bloat_levels
END
$$;
