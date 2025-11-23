#DB procedures

sub _dbh {
  unless ($_dbh && ref $_dbh && $_dbh->ping) {
    $_dbh = db_connect($_current_db_name, $db_host, $db_port, $db_user, $db_passwd);
    exit(0) unless ($_dbh && ref $_dbh && $_dbh->ping);
  }
  return $_dbh;
}

sub _after_round_statement {
  unless ($_after_round_statement) {
    if ($after_round_query) {
      $_after_round_statement = _dbh->prepare($after_round_query);

      if ($DBI::err) {
        logger(LOG_WARNING, "SQL Error in after round query %s: %s", $after_round_query, $DBI::errstr);
        unset $_after_round_statement;
      }
    }
  }
  return $_after_round_statement;
}

sub db_connect {
  my $db_name = shift;
  my $db_host = shift;
  my $db_port = shift;
  my $db_user = shift;
  my $db_password = shift;
  my $connstr = "DBI:Pg:";

  unset_after_round_statement;

  logger(LOG_WARNING, "Connecting to database");

  if ($db_name) {
    $connstr .= "dbname=$db_name;";
  }
  if ($db_host) {
    $connstr .= "host=$db_host;";
  }
  if ($db_port) {
    $connstr .= "port=$db_port;";
  }

  $_dbh = DBI->connect($connstr, $db_user, $db_password,{RaiseError => 0, PrintError => 0, AutoCommit => 1});

  if($DBI::err) { 
    logger(LOG_ERROR, "Cannot connect %s user=%s,passwd=...: %s", $connstr, $db_user, $DBI::errstr);
    return undef;
  }

  $_dbh->do("set client_min_messages to warning;");
  $_dbh->do("set lc_messages TO 'C';");
   $_dbh->do("set application_name TO pgcompacttable;");
  ($_dbh_server_version) = _dbh->selectrow_array("SHOW server_version_num");
  return $_dbh;
}

sub db_disconnect {
  my $db_name = shift;
  logger(LOG_WARNING, "Disconnecting from database");
  _dbh->disconnect;
}

sub get_databases {
  my $sth = _dbh->prepare("
SELECT datname FROM pg_catalog.pg_database
WHERE
    datname NOT IN ('template0')
ORDER BY pg_catalog.pg_database_size(datname), datname
  ");
  $sth->execute;
  
  if ($DBI::err) {
    logger(LOG_ERROR, "SQL Error: %s", $DBI::errstr);
    return undef;
  }

  my @result;
  while(my ($db_name) = $sth->fetchrow_array) {
    push @result, $db_name;
  }

  return \@result || [];
}

sub is_table {
    my $schema_name = shift;
    my $table_name = shift;

    my $sth = _dbh->prepare("
SELECT exists(
    SELECT 1 FROM pg_catalog.pg_tables
    WHERE schemaname = ? and tablename = ?
)
");

    $sth->execute($schema_name, $table_name);

    if ($DBI::err) {
      logger(LOG_ERROR, "SQL Error: %s", $DBI::errstr);
      return undef;
    }

    my ($result) = $sth->fetchrow_array;
    return $result;
}

sub get_database_tables {
  my $database_name = shift;
  my $table_names_like = shift;

  my $extra_conditions = '';
  if ($table_names_like) {
    $extra_conditions .= " AND tablename LIKE " . _dbh->quote($table_names_like);
  }
  if ($table_size_min > 0) {
      $extra_conditions .= " AND pg_catalog.pg_relation_size(
        quote_ident(schemaname) || '.' || quote_ident(tablename)) >= " . ($table_size_min * 1024 * 1024);
  }
  if ($table_size_max > 0) {
      $extra_conditions .= " AND pg_catalog.pg_relation_size(
        quote_ident(schemaname) || '.' || quote_ident(tablename)) < " . ($table_size_max * 1024 * 1024);
  }

  my $sth = _dbh->prepare("
SELECT schemaname, tablename FROM pg_catalog.pg_tables
WHERE
    NOT (schemaname = 'pg_catalog' AND tablename = 'pg_index') AND
    schemaname !~ 'pg_(temp|toast|catalog).*' AND
    NOT schemaname = 'information_schema'
    $extra_conditions
ORDER BY
    pg_catalog.pg_relation_size(
        quote_ident(schemaname) || '.' || quote_ident(tablename)),
    schemaname, tablename 
  ");
  
  if ($DBI::err) {
    logger(LOG_ERROR, "SQL Error: %s", $DBI::errstr);
    return undef;
  }

  $sth->execute;
  my @result;
  while(my $ident = $sth->fetchrow_hashref) {
    push @result, $ident;
  }
  return \@result || [];
}

sub get_pgstattuple_schema_name {
  my $sth = _dbh->prepare("
SELECT nspname FROM pg_catalog.pg_proc
JOIN pg_catalog.pg_namespace AS n ON pronamespace = n.oid
WHERE proname = 'pgstattuple' LIMIT 1
");
  $sth->execute;

  if ($DBI::err) {
    logger(LOG_ERROR, "SQL Error: %s", $DBI::errstr);
    return undef;
  }

  my ($pgstattuple_schema_name) = $sth->fetchrow_array; 
  return $pgstattuple_schema_name;
}

sub get_size_stats {
  my $schema_name = shift;
  my $table_name = shift;
  
  my $sth = _dbh->prepare("
SELECT
    size,
    total_size,
    ceil(size / bs) AS page_count,
    ceil(total_size / bs) AS total_page_count
FROM (
    SELECT
        current_setting('block_size')::integer AS bs,
        pg_catalog.pg_relation_size(quote_ident(?)||'.'||quote_ident(?)) AS size,
        pg_catalog.pg_total_relation_size(quote_ident(?)||'.'||quote_ident(?)) AS total_size
) AS sq
");

  $sth->execute($schema_name, $table_name, $schema_name, $table_name);

  if ($DBI::err) {
    logger(LOG_ERROR, "SQL Error: %s", $DBI::errstr);
    return undef;
  }

  my $result = $sth->fetchrow_hashref;
  
  if (! $result || ref $result ne 'HASH') {
    logger(LOG_ERROR,"Cannot get size statistics");
  }
  
  return $result;
}

sub get_bloat_stats {
  my $schema_name = shift;
  my $table_name = shift;

  my $ident_name = $schema_name.".".$table_name;
 
  my $pgstattuple_schema_name = get_pgstattuple_schema_name;
 
  return undef unless($pgstattuple_schema_name);

  my $sth = _dbh->prepare("SELECT
    ceil((size - free_space - dead_tuple_len) * 100 / fillfactor / bs) AS effective_page_count,
            greatest(round(
                (100 * (1 - (100 - free_percent - dead_tuple_percent) / fillfactor))::numeric, 2
            ),0) AS free_percent,
            greatest(ceil(size - (size - free_space - dead_tuple_len) * 100 / fillfactor), 0) AS free_space
    FROM (
    SELECT
        current_setting('block_size')::integer AS bs,
        pg_catalog.pg_relation_size(pg_catalog.pg_class.oid) AS size,
        coalesce(
            (
                SELECT (
                    regexp_matches(
                        reloptions::text, E'.*fillfactor=(\\\\d+).*'))[1]),
            '100')::real AS fillfactor,
        pgst.*
    FROM pg_catalog.pg_class
    CROSS JOIN
        " . _dbh->quote_identifier($pgstattuple_schema_name) . ".pgstattuple(
            (quote_ident(?) || '.' || quote_ident(?))) AS pgst
    WHERE pg_catalog.pg_class.oid = (quote_ident(?) || '.' || quote_ident(?))::regclass
    ) AS sq");
  $sth->execute($schema_name, $table_name, $schema_name, $table_name);

  if ($DBI::err) {
    logger(LOG_ERROR, "SQL Error: %s", $DBI::errstr);
    return undef;
  }

  my $result = $sth->fetchrow_hashref;
 
  return $result;
}

sub get_update_column {
  my $schema_name = shift;
  my $table_name = shift;

  my $sth = _dbh->prepare("SELECT quote_ident(attname)
    FROM pg_catalog.pg_attribute
    WHERE
    attnum > 0 AND -- neither system
    NOT attisdropped AND -- nor dropped
    attrelid = (quote_ident(?) || '.' || quote_ident(?))::regclass
    ORDER BY
    -- Variable legth attributes have lower priority because of the chance
    -- of being toasted
    (attlen = -1),
    -- Preferably not indexed attributes
    (
        attnum::text IN (
            SELECT regexp_split_to_table(indkey::text, ' ')
            FROM pg_catalog.pg_index
            WHERE indrelid = (quote_ident(?) || '.' || quote_ident(?))::regclass)),
    -- Preferably smaller attributes
    attlen,
    attnum
    LIMIT 1;");

  $sth->execute($schema_name, $table_name, $schema_name, $table_name);

  if ($DBI::err) {
    logger(LOG_ERROR, "SQL Error: %s", $DBI::errstr);
    return undef;
  }

  my ($result) = $sth->fetchrow_array;  
  return $result;
}

sub get_pages_per_round {
  my $page_count = shift;
  my $to_page = shift;

  my $real_pages_per_round = $page_count / PAGES_PER_ROUND_DIVISOR > 1 ? $page_count / PAGES_PER_ROUND_DIVISOR : 1; 
  my $pages_per_round = $real_pages_per_round < MAX_PAGES_PER_ROUND ? $real_pages_per_round : MAX_PAGES_PER_ROUND;
  my $result = ceil($pages_per_round) < $to_page ?  ceil($pages_per_round) : $to_page; 

  return $result;
}

sub get_pages_before_vacuum {
  my $page_count = shift;
  my $expected_page_count = shift;

  my $pages = $page_count / PAGES_BEFORE_VACUUM_LOWER_DIVISOR < PAGES_BEFORE_VACUUM_LOWER_THRESHOLD ? $page_count / PAGES_BEFORE_VACUUM_LOWER_DIVISOR : $page_count / PAGES_BEFORE_VACUUM_LOWER_THRESHOLD;
  my $result = $pages > $expected_page_count / PAGES_BEFORE_VACUUM_UPPER_DIVISOR ? $pages : $expected_page_count / PAGES_BEFORE_VACUUM_UPPER_DIVISOR;

  return ceil($result);
}

sub get_max_tupples_per_page {
  my $schema_name = shift;
  my $table_name = shift;

  my $ident_name = _dbh->quote_identifier($schema_name) . "." . _dbh->quote_identifier($table_name);

  my $sth = _dbh->prepare("
          SELECT ceil(current_setting('block_size')::real / sum(attlen))
          FROM pg_catalog.pg_attribute
          WHERE
              attrelid = '$ident_name'::regclass AND
              attnum < 0;
              ");
  $sth->execute;

  if ($DBI::err) {
    logger(LOG_ERROR, "SQL Error: %s", $DBI::errstr);
    return undef;
  }

  my ($result) = $sth->fetchrow_array; 

  logger(LOG_ERROR, 'Can not get max tupples per page.') unless(defined $result);

  return $result;
}

sub has_triggers {
  my $schema_name = shift;
  my $table_name = shift;

  my $ident_name = _dbh->quote_identifier($schema_name) . "." . _dbh->quote_identifier($table_name);

  my $sth = _dbh->prepare("SELECT count(1) FROM pg_catalog.pg_trigger
  WHERE
      tgrelid = '$ident_name'::regclass AND
      tgenabled IN ('A', 'R') AND
      (tgtype & 16)::boolean");
  $sth->execute;

  if ($DBI::err) {
    logger(LOG_ERROR, "SQL Error: %s", $DBI::errstr);
    return undef;
  }

  my ($result) = $sth->fetchrow_array;

  return $result;
}

sub try_advisory_lock {
  my $schema_name = shift;
  my $table_name = shift;
 
  my $sth = _dbh->prepare("
  SELECT pg_try_advisory_lock(
    'pg_catalog.pg_class'::regclass::integer,
    (quote_ident(?)||'.'||quote_ident(?))::regclass::integer)::integer;
    ");
  $sth->execute($schema_name, $table_name);

  if ($DBI::err) {
    logger(LOG_ERROR, "SQL Error: %s", $DBI::errstr);
    return undef;
  }

  my ($lock) = $sth->fetchrow_array;

  logger(LOG_NOTICE, "Skipping processing: another instance is working with table %s.%s", $schema_name, $table_name) unless ($lock); 
  
  return $lock;
}

sub vacuum {
  my $schema_name = shift;
  my $table_name = shift;
  my $analyze = shift; 
  my @vacuumopts = ();

  push(@vacuumopts, 'ANALYZE') if $analyze;
  push(@vacuumopts, 'INDEX_CLEANUP ON') if $_dbh_server_version >= 120000;

  my $sth = _dbh->do('VACUUM '.(@vacuumopts ? '('.join(',', @vacuumopts).') ' : ' '). _dbh->quote_identifier($schema_name) . "." . _dbh->quote_identifier($table_name));
  
  if ($DBI::err) {
    logger(LOG_ERROR, "SQL Error: %s", $DBI::errstr);
    return undef;
  }

  return;
}

sub analyze {
  my $schema_name = shift;
  my $table_name = shift;

  my $sth = $_dbh->do("ANALYZE "._dbh->quote_identifier($schema_name) . "." . _dbh->quote_identifier($table_name));

  if ($DBI::err) {
    logger(LOG_ERROR, "SQL Error: %s", $DBI::errstr);
    return undef;
  }

  return;
}

sub set_session_replication_role {
  my $sth = $_dbh->do('set session_replication_role to replica;');

  if ($DBI::err) {
    logger(LOG_ERROR, "SQL Error: %s", $DBI::errstr);
    return undef;
  }

  return;
}

sub create_clean_pages_function {
  
  _dbh->do("
CREATE OR REPLACE FUNCTION public.pgcompact_clean_pages_$$(
    i_table_ident text,
    i_column_ident text,
    i_to_page integer,
    i_page_offset integer,
    i_max_tupples_per_page integer)
RETURNS integer
LANGUAGE plpgsql AS \$\$
DECLARE
    _from_page integer := i_to_page - i_page_offset + 1;
    _min_ctid tid;
    _max_ctid tid;
    _ctid_list tid[];
    _next_ctid_list tid[];
    _ctid tid;
    _loop integer;
    _result_page integer;
    _update_query text :=
        'UPDATE ONLY ' || i_table_ident ||
        ' SET ' || i_column_ident || ' = ' || i_column_ident ||
        ' WHERE ctid = ANY(\$1) RETURNING ctid';
BEGIN
    -- Check page argument values
    IF NOT (
        i_page_offset IS NOT NULL AND i_page_offset >= 1 AND
        i_to_page IS NOT NULL AND i_to_page >= 1 AND
        i_to_page >= i_page_offset)
    THEN
        RAISE EXCEPTION 'Wrong page arguments specified.';
    END IF;

    -- Check that session_replication_role is set to replica to
    -- prevent triggers firing
    IF NOT (
        SELECT setting = 'replica'
        FROM pg_catalog.pg_settings
        WHERE name = 'session_replication_role')
    THEN
        RAISE EXCEPTION 'The session_replication_role must be set to replica.';
    END IF;

    -- Define minimal and maximal ctid values of the range
    _min_ctid := (_from_page, 1)::text::tid;
    _max_ctid := (i_to_page, i_max_tupples_per_page)::text::tid;

    -- Build a list of possible ctid values of the range
    SELECT array_agg((pi, ti)::text::tid)
    INTO _ctid_list
    FROM generate_series(_from_page, i_to_page) AS pi
    CROSS JOIN generate_series(1, i_max_tupples_per_page) AS ti;

    <<_outer_loop>>
    FOR _loop IN 1..i_max_tupples_per_page LOOP
        _next_ctid_list := array[]::tid[];

        -- Update all the tuples in the range
        FOR _ctid IN EXECUTE _update_query USING _ctid_list
        LOOP
            IF _ctid > _max_ctid THEN
                _result_page := -1;
                EXIT _outer_loop;
            ELSIF _ctid >= _min_ctid THEN
                -- The tuple is still in the range, more updates are needed
                _next_ctid_list := _next_ctid_list || _ctid;
            END IF;
        END LOOP;

        _ctid_list := _next_ctid_list;

        -- Finish processing if there are no tupples in the range left
        IF coalesce(array_length(_ctid_list, 1), 0) = 0 THEN
            _result_page := _from_page - 1;
            EXIT _outer_loop;
        END IF;
    END LOOP;

    -- No result
    IF _loop = i_max_tupples_per_page AND _result_page IS NULL THEN
        RAISE EXCEPTION
            'Maximal loops count has been reached with no result.';
    END IF;

    RETURN _result_page;
END \$\$;
  ");

  if ($DBI::err) {
    logger(LOG_ERROR, "SQL Error: %s", $DBI::errstr);
    return undef;
  }

  return 1;
}

sub drop_clean_pages_function {
  _dbh->do("
    DROP FUNCTION public.pgcompact_clean_pages_$$(text, text,integer, integer, integer);
    ");
  if ($DBI::err) {
    logger(LOG_ERROR, "SQL Error: %s", $DBI::errstr);
    return undef;
  }

  return ;
}

sub clean_pages {
  my $schema_name = shift;
  my $table_name = shift;
  my $column_name = shift;
  my $to_page = shift;
  my $pages_per_round = shift;
  my $max_tupples_per_page = shift;

  my $ident_name = _dbh->quote_identifier($schema_name) . "." . _dbh->quote_identifier($table_name);
  my $sth = _dbh->prepare("
    SELECT public.pgcompact_clean_pages_$$(?,?,?,?,?)
  ");
  $sth->execute($ident_name, $column_name, $to_page,  $pages_per_round, $max_tupples_per_page);

  if ($DBI::err) {
    logger(LOG_ERROR, "SQL Error: %s", $DBI::errstr);
    return undef;
  }
  
  my ($result) = $sth->fetchrow_array;

  return $result;
}

sub get_index_data_list {
  my $schema_name = shift;
  my $table_name = shift;

  my $sth = _dbh->prepare("
SELECT
    indexname, tablespace, indexdef,
    regexp_replace(indexdef, E'.* USING (\\\\w+) .*', E'\\\\1') AS indmethod,
    conname,
    CASE
        WHEN contype = 'p' THEN 'PRIMARY KEY'
        WHEN contype = 'u' THEN 'UNIQUE'
        ELSE NULL END AS contypedef,
    (
        SELECT
            bool_and(
                deptype IN ('n', 'a', 'i') AND
                NOT (refobjid = indexoid AND deptype = 'n') AND
                NOT (
                    objid = indexoid AND deptype = 'i'"
                    . ($_dbh_server_version < 90100 ? " AND contype NOT IN ('p', 'u')":"") . "
                ))
        FROM pg_catalog.pg_depend
        LEFT JOIN pg_catalog.pg_constraint ON
            pg_catalog.pg_constraint.oid = refobjid
        WHERE
            (objid = indexoid AND classid = pgclassid) OR
            (refobjid = indexoid AND refclassid = pgclassid)
    )::integer AS replace_index_possible,
    (
        SELECT string_to_array(indkey::text, ' ')::int2[] operator(pg_catalog.@>) array[0::int2]
        FROM pg_catalog.pg_index
        WHERE indexrelid = indexoid
    )::integer as is_functional,
    condeferrable as is_deferrable,
    condeferred as is_deferred,
    (contype = 'x') as is_exclude_constraint,
    pg_catalog.pg_relation_size(indexoid) as idxsize
FROM (
    SELECT i.relname AS indexname,
        (SELECT spcname AS tablespace FROM pg_catalog.pg_tablespace WHERE oid = (
            case when i.reltablespace != 0 then i.reltablespace else
                (SELECT dattablespace
                    FROM pg_catalog.pg_database
                    WHERE datname = current_database() AND
                          spcname != current_setting('default_tablespace'))
            end)
        ) as tablespace,
        pg_get_indexdef(i.oid) AS indexdef,
        i.oid as indexoid,
        'pg_catalog.pg_class'::regclass AS pgclassid
    FROM pg_index x
        JOIN pg_class c ON c.oid = x.indrelid
        JOIN pg_class i ON i.oid = x.indexrelid
        LEFT JOIN pg_namespace n ON n.oid = c.relnamespace
    WHERE c.relkind IN ('r', 'm', 'p') AND i.relkind IN ('i', 'I') AND
          n.nspname = ? AND
          c.relname = ?
) AS sq
LEFT JOIN pg_catalog.pg_constraint ON
    conindid = indexoid AND contype IN ('p', 'u', 'x')
ORDER BY idxsize
 ");
  
  $sth->execute($schema_name, $table_name);

  if ($DBI::err) {
    logger(LOG_ERROR, "SQL Error: %s", $DBI::errstr);
    return undef;
  }

  my @result;
  while(my $result = $sth->fetchrow_hashref) {
    push @result, $result;
  }

  return \@result;
}

sub get_index_size_statistics {
  my $schema_name = shift;
  my $index_name = shift;

  my $sth = _dbh->prepare("
SELECT size, ceil(size / bs) AS page_count
FROM (
    SELECT
        pg_catalog.pg_relation_size((quote_ident(?) || '.' || quote_ident(?))::regclass) AS size,
        current_setting('block_size')::real AS bs
) AS sq
  ");
  
  $sth->execute($schema_name, $index_name);

  if ($DBI::err) {
    logger(LOG_ERROR, "SQL Error: %s", $DBI::errstr);
    return undef;
  }

  my $result = $sth->fetchrow_hashref;
  
  return ($result && ref $result eq 'HASH' && defined $result->{size} && defined $result->{page_count} ? $result : undef);
}

sub get_reindex_query {
  my $index_data = shift;  

  my $sql = $index_data->{indexdef};
  $sql =~ s/INDEX (\S+)/INDEX CONCURRENTLY pgcompact_index_$$/;
  $sql =~ s/( WHERE .*|$)/ TABLESPACE $index_data->{tablespace}$1/ if (defined $index_data->{tablespace});

  return $sql;

}

sub get_alter_index_query {
  my $schema_name = shift;
  my $table_name = shift;
  my $index_data = shift;

  my $constraint_ident = _dbh->quote_identifier($index_data->{conname}) if ($index_data && ref $index_data eq 'HASH' && $index_data->{conname});

  if($constraint_ident) {
    my $constraint_options = "$index_data->{contypedef} USING INDEX pgcompact_index_$$";
    if ($index_data->{is_deferrable}) {
        $constraint_options .= " DEFERRABLE";
    }
    if ($index_data->{is_deferred}) {
        $constraint_options .= " INITIALLY DEFERRED";
    }
    return 
    "BEGIN; SET LOCAL statement_timeout TO " . $reindex_lock_timeout . ";
ALTER TABLE " . _dbh->quote_identifier($schema_name) . "." . _dbh->quote_identifier($table_name) . " DROP CONSTRAINT $constraint_ident;
ALTER TABLE " . _dbh->quote_identifier($schema_name) . "." . _dbh->quote_identifier($table_name) . " ADD CONSTRAINT $constraint_ident $constraint_options;
END;";
  } else {
    my $tmp_index_name = "tmp_".int(rand(1000000000));
    return
    "BEGIN; SET LOCAL statement_timeout TO " . $reindex_lock_timeout . ";
ALTER INDEX " . _dbh->quote_identifier($schema_name) . "." . _dbh->quote_identifier($index_data->{indexname}) . " RENAME TO " . _dbh->quote_identifier($tmp_index_name) . ";
ALTER INDEX " . _dbh->quote_identifier($schema_name) . ".pgcompact_index_$$ RENAME TO " . _dbh->quote_identifier($index_data->{indexname}) . ";
END;
DROP INDEX CONCURRENTLY " . _dbh->quote_identifier($schema_name) . "." . _dbh->quote_identifier($tmp_index_name) . ";";
  }
}

sub get_straight_reindex_query {
  my $schema_name = shift;
  my $table_name = shift;
  my $index_data = shift;

  return "REINDEX INDEX ('" . _dbh->quote_identifier($schema_name) . "." . _dbh->quote_identifier($index_data->{indexname})."')";
}

sub get_index_bloat_stats {
  my $schema_name = shift;
  my $index_name = shift;

  my $pgstattuple_schema_name = get_pgstattuple_schema_name;

  return undef unless($pgstattuple_schema_name);  
  
  my $sth = _dbh->prepare("
SELECT
    CASE
        WHEN avg_leaf_density = 'NaN' THEN 0
        ELSE
            round(
                (100 * (1 - avg_leaf_density / fillfactor))::numeric, 2
            )
        END AS free_percent,
    CASE
        WHEN avg_leaf_density = 'NaN' THEN 0
        ELSE
            ceil(
                index_size * (1 - avg_leaf_density / fillfactor)
            )
        END AS free_space
FROM (
    SELECT
        coalesce(
            (
                SELECT (
                    regexp_matches(
                        reloptions::text, E'.*fillfactor=(\\\\d+).*'))[1]),
            '90')::real AS fillfactor,
        pgsi.*
    FROM pg_catalog.pg_class
    CROSS JOIN $pgstattuple_schema_name.pgstatindex(
        quote_ident(?) || '.' || quote_ident(?)) AS pgsi
    WHERE pg_catalog.pg_class.oid = (quote_ident(?) || '.' || quote_ident(?))::regclass
) AS oq
  ");
  $sth->execute($schema_name, $index_name, $schema_name, $index_name);

  if ($DBI::err) {
    logger(LOG_ERROR, "SQL Error: %s", $DBI::errstr);
    return undef;
  }

  my $result = $sth->fetchrow_hashref;

  return ($result && ref $result eq 'HASH' && $result->{'free_percent'} && $result->{'free_space'}) ? $result : undef;
}

sub reindex {
  my $index_data = shift;

  _dbh->do(get_reindex_query($index_data));

  if ($DBI::err) {
    logger(LOG_ERROR, "SQL Error: %s", $DBI::errstr);
    return undef;
  }

  return;
}

sub alter_index {
  my $schema_name = shift;
  my $table_name = shift;
  my $index_data = shift;
  
  foreach my $sql (split(/;/, get_alter_index_query($schema_name, $table_name, $index_data))) {
    next if ($sql =~ /^\s*$/);
    _dbh->do("$sql;");

    if ($DBI::err) {
      return undef;
    }

  }
}

sub drop_temp_index {
  my $schema_name = shift;

  _dbh->do("DROP INDEX CONCURRENTLY " . _dbh->quote_identifier($schema_name) . "." . _dbh->quote_identifier("pgcompact_index_$$") . ";");

  if ($DBI::err) {
    logger(LOG_ERROR, "Unable remove temporary index pgcompact_index_$$");
    logger(LOG_ERROR, "SQL Error: %s", $DBI::errstr);
    return undef;
  }

  return;
}

sub get_pg_backend_pid {
  my ($backend_pid) = _dbh->selectrow_array("select pg_backend_pid();");
  return $backend_pid;
}

sub reindex_index_concurrently {
    my $index_data = shift;
    my $schema_name = shift;
    my $table_name = shift;
    my $initial_index_size_stats = shift;

    my $start_reindex_time = time;

    _dbh->do("REINDEX INDEX CONCURRENTLY " . _dbh->quote_identifier($schema_name) . "." . _dbh->quote_identifier($index_data->{indexname}));

    my $reindex_time = time - $start_reindex_time;

    if ($DBI::err) {
      logger(LOG_ERROR, "SQL Error: %s", $DBI::errstr);
      return 0;
    }

    my $new_stats = get_index_size_statistics($schema_name, $index_data->{indexname});
    my $free_percent = 100 * (1 - $new_stats->{size} / $initial_index_size_stats->{size});
    my $free_space = ($initial_index_size_stats->{size} - $new_stats->{size});
    logger(LOG_WARNING, "Reindex%s: %s.%s, initial size %d pages(%s), has been reduced by %d%% (%s), duration %d seconds.",
        ($force ? " forced" : ""),
        $schema_name,
        $index_data->{indexname},
        $initial_index_size_stats->{page_count},
        nice_size($initial_index_size_stats->{size}),
        int($free_percent),
        nice_size($free_space),
        $reindex_time
    );

    return 1;
}

sub reindex_index_old_replace {
    my $index_data = shift;
    my $schema_name = shift;
    my $table_name = shift;
    my $initial_index_size_stats = shift;

    my $start_reindex_time = time;

    reindex($index_data);

    if($DBI::err) {
      logger(LOG_NOTICE, "Skipping index %s: %s", $index_data->{indexname}, $DBI::errstr);
      drop_temp_index($schema_name);
      next;
    }

    my $reindex_time = time - $start_reindex_time;

    if ($index_data->{is_functional}) {
      # perform auto analyze for functional indexes
      my $analyze_time = time;
      analyze($schema_name, $table_name);

      if ($DBI::err) {
        logger(LOG_ERROR, "Autoanalyze functional index error");
        drop_temp_index($schema_name);
        next;
      }

      $analyze_time = time - $analyze_time;
      logger(LOG_NOTICE, "Autoanalyze functional index: duration %.3f second.", $analyze_time);
    }

    my $locked_alter_attempt = 0;
    while ($locked_alter_attempt < $reindex_retry_max_count) {
      unless(defined(alter_index($schema_name, $table_name, $index_data))) {
        my $db_errstr = $DBI::errstr;

        _dbh->do("END;");

        if ($db_errstr && $db_errstr =~ 'canceling statement due to statement timeout') {
          $locked_alter_attempt++;
          logger(LOG_NOTICE, "Reindex%s: %s.%s, lock retry %d",
              ($force ? " forced" : ""),
              $schema_name,
              $index_data->{indexname},
              $locked_alter_attempt
          );
          if ($reindex_retry_pause) {
              sleep($reindex_retry_pause);
          }
          next;
        } else {
          logger(LOG_ERROR, "SQL Error: %s", $db_errstr);
          logger(LOG_ERROR, $@);
          return;
        }
      };
      $reindex_time = time - $start_reindex_time;
      last;
    }

    if ($locked_alter_attempt < $reindex_retry_max_count) {
          my $new_stats = get_index_size_statistics($schema_name, $index_data->{indexname});
          my $free_percent = 100 * (1 - $new_stats->{size} / $initial_index_size_stats->{size});
          my $free_space = ($initial_index_size_stats->{size} - $new_stats->{size});
          logger(LOG_WARNING, "Reindex%s: %s.%s, initial size %d pages(%s), has been reduced by %d%% (%s), duration %d seconds, attempts %d.",
            ($force ? " forced" : ""),
            $schema_name,
            $index_data->{indexname},
            $initial_index_size_stats->{page_count},
            nice_size($initial_index_size_stats->{size}),
            int($free_percent),
            nice_size($free_space),
            $reindex_time,
            $locked_alter_attempt
          );
        return 1;
        #~ $is_reindexed = (defined $is_reindexed) ? ($is_reindexed and 1) : 1;
      } else {
        logger(LOG_NOTICE, "Reindex%s: %s.%s, unable lock, delete index",
          ($force ? " forced" : ""),
          $schema_name,
          $index_data->{indexname}
        );
        my $drop_index_time = time;
        drop_temp_index($schema_name);
        $reindex_time += time - $drop_index_time;
        logger(LOG_WARNING, "Reindex%s: %s.%s, lock has not been acquired, initial size %d pages(%s)",#, can be reduced by %d%% (%s), duration %d seconds.",
          ($force ? " forced" : ""),
          $schema_name,
          $index_data->{indexname},
          $initial_index_size_stats->{page_count},
          nice_size($initial_index_size_stats->{size}),
          #$bloat_stats->{free_percent},
          #nice_size($bloat_stats->{free_space}),
          #$reindex_time
        );
        return 0;
        #~ $is_reindexed = 0;
      }
}

sub reindex_table {
  my $table_name = shift;
  my $schema_name = shift;
  my $db_name = shift;
  my $print_reindex_queries = shift;

  my $is_reindexed;

  my $use_reindex_concurrently = ($_dbh_server_version >= 120000);
  if ($reindex_replace) {
      $use_reindex_concurrently = 0;
  }

  my $index_data_list = get_index_data_list($schema_name, $table_name) || [];

  if ($DBI::err) {
    logger(LOG_ERROR, "Table handling interrupt.");
    return -1;
  }

  for my $index_data (@$index_data_list) {
    my $initial_index_size_stats = get_index_size_statistics($schema_name, $index_data->{indexname});

    if (!$initial_index_size_stats || ref $initial_index_size_stats ne 'HASH') {
      logger(LOG_ERROR, "Cannot get index size statistics.");
      return;
    }

    if ($initial_index_size_stats->{page_count} <= 1) {
      logger(LOG_NOTICE, "Skipping reindex: %s.%s, empty or 1 page index.", $schema_name, $index_data->{indexname});
      next;
    }

    if ($index_data->{'is_exclude_constraint'}) {
      logger(LOG_NOTICE, "Skipping reindex: %s.%s, can not reindex exclusion constraints", $schema_name, $index_data->{indexname});
      next;
    }

    my $index_bloat_stats;
    
    if (! $force) {
      if ($index_data->{indmethod} ne 'btree') {
        logger(LOG_NOTICE, "Skipping reindex: %s.%s is a %s index not a btree, reindexing is up to you.", $schema_name, $index_data->{indexname}, $index_data->{indmethod});
        logger(LOG_WARNING, "Reindex queries: %s.%s, initial size %d pages (%s)", $schema_name, $index_data->{indexname}, $initial_index_size_stats->{page_count}, nice_size($initial_index_size_stats->{size}));
        if ($index_data->{data}{'replace_index_possible'}) {
          logger(LOG_WARNING, "%s; --%s", get_reindex_query($index_data), $db_name);
          logger(LOG_WARNING, "%s; --%s", get_alter_index_query($schema_name, $table_name, $index_data), $db_name);
        } else {
          logger(LOG_WARNING, "%s; --%s", get_straight_reindex_query($schema_name, $table_name, $index_data), $db_name);
        }
        next;
      }

      if ($initial_index_size_stats->{page_count} < MINIMAL_COMPACT_PAGES) {
        logger(LOG_NOTICE, "Skipping reindex: %s.%s, %d pages from %d pages minimum required.",$schema_name, $index_data->{indexname}, $initial_index_size_stats->{page_count}, MINIMAL_COMPACT_PAGES);
        next;
      }

      $index_bloat_stats = get_index_bloat_stats($schema_name, $index_data->{indexname});

      if ($index_bloat_stats && ref $index_bloat_stats eq 'HASH' && $index_bloat_stats->{'free_percent'} < MINIMAL_COMPACT_PERCENT) {
        logger(LOG_NOTICE, "Skipping reindex: %s.%s, %d%% space to compact from %d%% minimum required.", $schema_name, $index_data->{indexname}, $index_bloat_stats->{free_percent}, MINIMAL_COMPACT_PERCENT);
        next;
      }
    }

    if (not $index_data->{'replace_index_possible'} and not $use_reindex_concurrently) {
      logger(LOG_NOTICE, "Skipping reindex: %s.%s, can not reindex without heavy locks because of its dependencies, reindexing is up to you.", $schema_name, $index_data->{indexname});
      logger(LOG_WARNING, "Reindex queries%s: %s.%s, initial size %d pages (%s), will be reduced by %d%% (%s)",
         ($force ? ' forced' : ''),
         $schema_name,
         $index_data->{indexname},
         $initial_index_size_stats->{page_count},
         nice_size($initial_index_size_stats->{size}),
         $index_bloat_stats->{free_percent},
         nice_size($index_bloat_stats->{free_space})
      );
      logger(LOG_WARNING, "%s; --%s", get_reindex_query($index_data), $db_name);
      logger(LOG_WARNING, "%s; --%s", get_alter_index_query($schema_name, $table_name, $index_data), $db_name);

      next;
    }

    if (!$no_reindex) {
      my $reindex_result;
      if ($use_reindex_concurrently) {
        $reindex_result = reindex_index_concurrently($index_data, $schema_name, $table_name, $initial_index_size_stats);
      } else {
        $reindex_result = reindex_index_old_replace($index_data, $schema_name, $table_name, $initial_index_size_stats);
      }
      $is_reindexed = (defined $is_reindexed) ? ($is_reindexed and $reindex_result) : $reindex_result;
    }

    if ($print_reindex_queries) {
      logger(LOG_WARNING, "Reindex queries%s: %s.%s, initial size %d pages (%s), will be reduced by %d%% (%s)",
        ($force ? ' forced' : ''),
        $schema_name,
        $index_data->{indexname},
        $initial_index_size_stats->{page_count},
        nice_size($initial_index_size_stats->{size}),
        ($index_bloat_stats->{free_percent}||0),
        nice_size($index_bloat_stats->{free_space})
      );

      logger(LOG_WARNING, "%s; --%s", get_reindex_query($index_data), $db_name);
      logger(LOG_WARNING, "%s; --%s", get_alter_index_query($schema_name, $table_name, $index_data), $db_name);
    }
  }
  
  return $is_reindexed;
}
