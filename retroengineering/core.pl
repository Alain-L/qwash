foreach my $current_db_name (@dbs) {
  $table_info{$current_db_name} = {tables => {}, attempts => 0, size => 0, base_size => 0};
  $db_name = $current_db_name;

  # Préparation : connexion à la BDD
  set_current_db_name($current_db_name);
  unset_current_schema_name_table_name;
  unless(db_connect($current_db_name, $db_host, $db_port, $db_user, $db_passwd)) {
    $databases_left++;
    next;
  }

  # Tente de mettre le backend PostgreSQL en priorité basse (ionice)
  my $backend_pid = get_pg_backend_pid();
  if ($backend_pid) {
    my $user_login = $ENV{LOGNAME} || $ENV{USER} || getpwuid($<);
    if ($user_login eq 'postgres' || $user_login eq 'root') {
      my $errstr = `ionice -c 3 -p $backend_pid 2>/dev/stdout`;
      # S’il échoue, on logue un avertissement
    }
  }

  # session_replication_role = replica
  set_session_replication_role;

  # Vérifie la présence de l’extension pgstattuple
  unless(get_pgstattuple_schema_name) {
    logger('qiuet', "Skip handling database %s: pgstattuple extention is not found", $current_db_name);
    db_disconnect($current_db_name);
    next;
  }

  # Crée une fonction temporaire utile à la détection de pages vides
  unless (create_clean_pages_function) {
    logger('qiuet', "Skip handling database %s: pgstattuple cannot create clean_pages function", $current_db_name);
    db_disconnect($current_db_name);
    $databases_left++;
    next;
  }

  # Récupération de la liste des tables à traiter
  my $database_tables = [];
  if ($schema_name && $table_name) {
    if (is_table($schema_name, $table_name)) {
      $database_tables = [{schemaname => $schema_name, tablename => $table_name}];
    }
  } else {
    $database_tables = get_database_tables($current_db_name, $table_names_like);
  }

  unless($database_tables && ref $database_tables eq 'ARRAY' && scalar(@$database_tables) > 0) {
    logger('qiuet', "Skip handling database %s: cannot find tables", $current_db_name);
  }

  # Tentatives (jusqu’à N fois) de compacter les tables
  for (my $attempt = 0; $attempt < $max_retry_count; $attempt++) {
    
    logger('qiuet', "Handling tables. Attempt %s", ($attempt + 1));
    $table_info{$current_db_name}{attempts}++;
    
    my @retry_idents = ();

    # Pour chaque table :
    foreach my $current_ident (@$database_tables) {
      # Filtres (schemas exclus, tables exclues, etc.)
      next if (!$current_ident || ref $current_ident ne 'HASH' || $excluded_schemas{$current_ident->{schemaname}} || ($only_schema && !$only_schemas{$current_ident->{schemaname}}) || $excluded_tables{"$current_ident->{schemaname}.$current_ident->{tablename}"});

      my $table_key = $current_ident->{schemaname}.$current_ident->{tablename};

      # Initialisation du suivi d’état
      $table_info{$current_db_name}{tables}{$table_key}{current} ||= {};

      set_current_schema_name_table_name($current_ident->{schemaname}, $current_ident->{tablename});
      logger(LOG_NOTICE, "Start handling table %s.%s", $current_ident->{schemaname}, $current_ident->{tablename});

      # Traitement de la table : on ajoute à retry si la tentative échoue
      unless (process($current_ident->{schemaname}, $current_ident->{tablename}, $attempt, $table_info{$current_db_name}{tables}{$table_key}{current})) {
        push @retry_idents, $current_ident;
      }

      logger(LOG_NOTICE, "Finish handling table %s.%s", $current_ident->{schemaname}, $current_ident->{tablename});

      # Stockage des statistiques avant/après pour calcul de gain
      if ($attempt == 0) {
        $table_info{$current_db_name}{tables}{$table_key}{final}{base_stats}{size} = $table_info{$current_db_name}{tables}{$table_key}{current}{base_stats}{size}; 
        $table_info{$current_db_name}{tables}{$table_key}{final}{base_stats}{total_size} = $table_info{$current_db_name}{tables}{$table_key}{current}{base_stats}{total_size};
      }
      $table_info{$current_db_name}{tables}{$table_key}{final}{stats}{size} = $table_info{$current_db_name}{tables}{$table_key}{current}{stats}{size}; 
      $table_info{$current_db_name}{tables}{$table_key}{final}{stats}{total_size} = $table_info{$current_db_name}{tables}{$table_key}{current}{stats}{total_size};
    }

    # Si on a encore des tables à traiter, on continue, sinon on quitte la boucle
    if (scalar @retry_idents > 0) {
      @$database_tables = @retry_idents;
    } else {
      undef $tables_left;
      last;
    }

    $tables_left = scalar(@retry_idents);
  }

  # Nettoyage et reporting
  drop_clean_pages_function;
  unset_current_schema_name_table_name;

  $databases_left++ if ($tables_left);

  logger(LOG_WARNING, "Processing %scomplete%s.", ($tables_left ? 'in' : ''), ($tables_left ? ": $tables_left tables left" : ''));

  # Calcul du gain de taille
  $table_info{$current_db_name}{size} = sum(map {
    $table_info{$current_db_name}{tables}{$_}{final}{base_stats}{size} -
    $table_info{$current_db_name}{tables}{$_}{final}{stats}{size}
  } keys(%{$table_info{$current_db_name}{tables}}));

  $table_info{$current_db_name}{total_size} = sum(map {
    $table_info{$current_db_name}{tables}{$_}{final}{base_stats}{total_size} -
    $table_info{$current_db_name}{tables}{$_}{final}{stats}{total_size}
  } keys(%{$table_info{$current_db_name}{tables}}));

  logger(LOG_ERROR, "Processing results: size reduced by %s (%s including toasts and indexes) in total.",
    nice_size($table_info{$current_db_name}{size}),
    nice_size($table_info{$current_db_name}{total_size}));

  db_disconnect($current_db_name);
}
