#Main code

if ($show_version) {
  show_version;
  exit(1);
}

if ($show_help) {
  show_help;
  exit(1);
}

if ($show_man) {
  show_man;
  exit(1);
}

unless ($db_name || $table_name || $all_db) {
  not_enough_arguments();
  exit(1);
}

if ($no_reindex && $initial_reindex) {
  no_together_arguments('no_reindex', 'initial_reindex');
  exit(1);
}

if ($delay_ratio < 0) {
  logger(LOG_ERROR, "Incorrect delay-ratio: negative time not invented yet.");
  exit(1);
}

my @dbs = ($db_name);

if($all_db) {
  $db_name = 'template1';
  unless(db_connect($db_name, $db_host, $db_port, $db_user, $db_passwd)) {
    logger(LOG_ERROR, "Cannot get database list. Quit.");
    exit(0);
  }
  @dbs = 
  my $dbs = get_databases;
  unless ($dbs) {
    logger(LOG_ERROR, "Interrupt processing.");
    exit(0);
  }
  @dbs = @$dbs;

  db_disconnect;
}

if($only_schema) {
  %only_schemas = map {$_ => 1} split(/,/,$only_schema);
  $schema_name = $only_schema if (scalar(keys(%only_schemas)) == 1);
}

if($exclude_schema) {
  %excluded_schemas = map {$_ => 1} split(/,/,$exclude_schema);
}

if($exclude_table) {
  %excluded_tables = map {$_ => 1} split(/,/,$exclude_table);
}

my $databases_left;
my $is_something_processed;

foreach my $current_db_name (@dbs) {
  $table_info{$current_db_name} = {tables => {}, attempts => 0, size => 0, base_size => 0};
  $db_name = $current_db_name; 
  my $tables_left;

  set_current_db_name($current_db_name);
  unset_current_schema_name_table_name;
  unless(db_connect($current_db_name, $db_host, $db_port, $db_user, $db_passwd)) {
    $databases_left++;
    next;
  }

  my $ionice_made = undef;

  my $backend_pid = get_pg_backend_pid();
  if ($backend_pid) {
    my $user_login = $ENV{LOGNAME} || $ENV{USER} || getpwuid($<);
    if ($user_login eq 'postgres' || $user_login eq 'root') {
      my $errstr = `ionice -c 3 -p $backend_pid 2>/dev/stdout`;
      if ($errstr) {
        chomp $errstr;
        logger(LOG_WARNING, "Cannot set ionice 3 for the process. It is recommended to set ionice -c 3 for pgcompacttable. Error: %s", $errstr);
      } else {
        $ionice_made = 1;
      }
    }
    logger(LOG_WARNING, "Postgres backend pid: $backend_pid") if ($backend_pid && $backend_pid =~/^\d+$/ && $backend_pid > 0);
  } else {
    logger(LOG_ERROR, "Cannot get backend pid from Postgres. Exitting...");
    exit(-1);
  }

  unless ($ionice_made) {
    logger(LOG_WARNING, "It is recommended to set ionice -c 3 for pgcompacttable: ionice -c 3 -p %d", $backend_pid);
  }

  set_session_replication_role;

  if ($DBI::err) {
    logger(LOG_ERROR, "Database handling interrupt.");
    db_disconnect($current_db_name);
    $databases_left++;
    next;
  }

  unless(get_pgstattuple_schema_name) {
    logger('qiuet', "Skip handling database %s: pgstattuple extention is not found", $current_db_name);
    db_disconnect($current_db_name);
    next;
  }

  unless (create_clean_pages_function) {
    logger('qiuet', "Skip handling database %s: pgstattuple cannot create clean_pages function", $current_db_name);
    db_disconnect($current_db_name);
    $databases_left++;
    next;
  }

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

  for (my $attempt = 0; $attempt < $max_retry_count; $attempt++) {
    
    logger('qiuet', "Handling tables. Attempt %s", ($attempt + 1));
    
    $table_info{$current_db_name}{attempts}++;
    
    my @retry_idents = ();

    foreach my $current_ident (@$database_tables) {
      next if (!$current_ident || ref $current_ident ne 'HASH' || $excluded_schemas{$current_ident->{schemaname}} || ($only_schema && !$only_schemas{$current_ident->{schemaname}}) || $excluded_tables{"$current_ident->{schemaname}.$current_ident->{tablename}"});
      my $table_key = $current_ident->{schemaname}.$current_ident->{tablename};
      $table_info{$current_db_name}{tables}{$table_key}{current} = {} unless ($table_info{$current_db_name}{tables}{$table_key}{current} && ref $table_info{$current_db_name}{tables}{$table_key}{current} eq 'HASH');
      $is_something_processed = 1 unless ($is_something_processed);
      set_current_schema_name_table_name($current_ident->{schemaname}, $current_ident->{tablename});
      logger(LOG_NOTICE, "Start handling table %s.%s", $current_ident->{schemaname}, $current_ident->{tablename});
      push @retry_idents, $current_ident unless process($current_ident->{schemaname}, $current_ident->{tablename}, $attempt, $table_info{$current_db_name}{tables}{$table_key}{current});
      logger(LOG_NOTICE, "Finish handling table %s.%s", $current_ident->{schemaname}, $current_ident->{tablename});
      if ($attempt == 0) {
        $table_info{$current_db_name}{tables}{$table_key}{final}{base_stats}{size} = $table_info{$current_db_name}{tables}{$table_key}{current}{base_stats}{size}; 
        $table_info{$current_db_name}{tables}{$table_key}{final}{base_stats}{total_size} = $table_info{$current_db_name}{tables}{$table_key}{current}{base_stats}{total_size};
      }
      $table_info{$current_db_name}{tables}{$table_key}{final}{stats}{size} = $table_info{$current_db_name}{tables}{$table_key}{current}{stats}{size}; 
      $table_info{$current_db_name}{tables}{$table_key}{final}{stats}{total_size} = $table_info{$current_db_name}{tables}{$table_key}{current}{stats}{total_size};
    }
    
    if (scalar @retry_idents > 0) {
      @$database_tables = @retry_idents;
    } else {
      undef $tables_left;
      last;
    }
    
    $tables_left = scalar(@retry_idents);
  }

  drop_clean_pages_function;

  unset_current_schema_name_table_name;

  $databases_left++ if ($tables_left);
  
  logger(LOG_WARNING, "Processing %scomplete%s.", ($tables_left ? 'in' : ''), ($tables_left ? ": $tables_left tables left" : ''));
  
  $table_info{$current_db_name}{size} = sum(map {$table_info{$current_db_name}{tables}{$_}{final}{base_stats}{size} - $table_info{$current_db_name}{tables}{$_}{final}{stats}{size}} keys(%{$table_info{$current_db_name}{tables}}));
  $table_info{$current_db_name}{total_size} = sum(map {$table_info{$current_db_name}{tables}{$_}{final}{base_stats}{total_size} - $table_info{$current_db_name}{tables}{$_}{final}{stats}{total_size}} keys(%{$table_info{$current_db_name}{tables}}));
  logger(LOG_ERROR, "Processing results: size reduced by %s (%s including toasts and indexes) in total.", nice_size($table_info{$current_db_name}{size}), nice_size($table_info{$current_db_name}{total_size}));

  db_disconnect($current_db_name);
}

unset_current_db_name;

if($databases_left) {
  logger(LOG_WARNING, "Processing incomplete: %d databases left.", $databases_left);
} else {
  logger(LOG_WARNING, "Processing complete: %d retries to process has been done", sum(map {$table_info{$_}{attempts}} keys(%table_info)));
}
  
if ($is_something_processed) {
  my $dbases_message = "";
  foreach (keys(%table_info)) {
    $dbases_message .= ", " . nice_size($table_info{$_}{size}) . " (" . nice_size($table_info{$_}{total_size}) . ") $_" if ($table_info{$_} && ref $table_info{$_} eq 'HASH' && $table_info{$_}{size} && $table_info{$_}{total_size});
  }

  logger(LOG_ERROR, "Processing results: size reduced by %s (%s including toasts and indexes) in total%s.",
    nice_size(sum(map {$table_info{$_}{size}||0} keys(%table_info)) || 0),
    nice_size(sum(map {$table_info{$_}{total_size}||0} keys(%table_info))|| 0),
    $dbases_message
  );
}

1;

=head1 NAME

B<pgcompacttable> - PostgreSQL bloat reducing tool.

=head1 SYNOPSIS

pgcompacttable [OPTION...]

=over 4

=item General options:

[-?mV] [(-q | -v LEVEL)]

=item Connection options:

[-h HOST] [-p PORT] [-U USER] [-W PASSWD] [-P PATH]

=item Targeting options:

(-a | -d DBNAME...) [-n SCHEMA...] [-t TABLE...]
[-N SCHEMA...] [-T TABLE...]

=back

=head1 DESCRIPTION

B<pgcompacttable> is a tool to reduce bloat for tables and indexes without
heavy locks.

=over 4

=back

pgstattuple must be installed. B<pgcompacttable> uses pgstattuple to get statistics. 

=head1 EXAMPLES

Shows usage manual.

  pgcompacttable --man
Compacts all the bloated tables in all the database in the cluster plus their bloated indexes. Prints additional progress information.

  pgcompacttable --all --verbose info

Compacts all the bloated tables in the billing database and their bloated indexes excepts ones that are in the pgq schema.

  pgcompacttable --dbname billing --exclude-schema pgq

=head1 OPTIONS

=head2 General options

=over 4

=item B<-?>

=item B<--help>

Display short help.

=item B<-m>

=item B<--man>

Display full manual.

=item B<-V>

=item B<--version>

Print version.

=item B<-q>

=item B<--quiet>

Quiet mode. Do not display any messages exept error messages and total result.

=item B<-v>

=item B<--verbose>

Verbose mode. Display all the progress messages.

=back

=head2 Connection options

The B<pgcompacttable> tries to connect to the database with the DBI Perl module. 

If some of the connection options is not specified the tool tries to
get it from C<PGHOST>, C<PGPORT>, C<PGUSER>, C<PGPASSWORD> environment
variables. If password is still unknown after that than it tries to
get it from the password file that C<PGPASSFILE> refers to and if this
file does not exist it tries to get it from C<HOME/.pgpass> file.

=over 4

=item B<-h> HOST

=item B<--host> HOST

database server host or socket directory

=item B<-p> PORT

=item B<--port> PORT

database server port

=item B<-U> USER

=item B<--user> USER

A database user. By default current system user is used (as returned by whoami).

=item B<-W> PASSWD

=item B<--password> PASSWD

A password for the user.

=back

=head2 Targeting options

Note that if you specified a database, schema or table that is not in the cluster it will be ignored. Redundant exclusions will be ignored too. All these options except C<--all> can be specified several times.

=over 4

=item B<-a>

=item B<--all>

Process all the databases in the cluster.

=item B<-d> DBNAME

=item B<--dbname> DBNAME

A database to process. By default all the user databses of the instance are processed.

=item B<-n> SCHEMA

=item B<--schema> SCHEMA

A schema to process. By default 'public' schema id processed.

=item B<-N> SCHEMA

=item B<--exclude-schema> SCHEMA

A schema to exclude from processing.

=item B<-t> TABLE

=item B<--table> TABLE

A table to process. By default all the tables of the specified schema are processed.

=item B<--tables-like> 'LIKE expression'

Condition to find tables to process by passing argument to SQL condition tablename LIKE ?. % for wildcard, _ for any one symbol. By default all the tables of the specified schema are processed.

=item B<-T> TABLE

=item B<--exclude-table> TABLE

A table to exclude from processing.

=item B<--min-table-size> SIZE

Tables smaller than the specified size (in megabytes) will be excluded from processing

=item B<--max-table-size> SIZE

Tables larger than the specified size (in megabytes) will be excluded from processing

=back

=head2 Options controlling the behaviour

=over 4

=item B<-R>

=item B<--routine-vacuum>

Turn on the routine vacuum. By default all the vacuums are off.

=item B<-r>

=item B<--no-reindex>

Turn off reindexing of tables after processing.

=item B<--no-initial-vacuum>

Turn off initial vacuum before table processing.

=item B<-i>

=item B<--initial-reindex>

Perform an initial reindex of tables before processing.

=item B<-s>

=item B<--print-reindex-queries>

Print reindex queries. Useful if you want to perform manual
reindex later.

=item B<--reindex-replace>

Avoid using REINDEX INDEX CONCURRENTLY even when it is available. By default this native PostgreSQL feature is preferred.

=item B<--reindex-retry-count>

Attempts count to concurrently safe replace bloated index to new. Default 100

=item B<--reindex-retry-pause>

Pause between reindex attempts in seconds. Default is 1 second

=item B<--reindex-lock-timeout>

Statement timeout for reindex ALTER TABLE queries. Default is 1000 (ms)

=item B<-f>

=item B<--force>

Try to compact even those tables and indexes that do not meet minimal bloat requirements.

=item B<-E> RATIO

=item B<--delay-ratio> RATIO

A dynamic part of the delay between rounds is calculated as previous-round-time * delay-ratio. By default 2.

=item B<-Q> Query

=item B<--after-round-query> Query

SQL statement to be called after each round against current database

=item B<-o> COUNT

=item B<--max-retry-count> COUNT

A maximum number of retries in case of unsuccessful processing. By default 10.

=back

=cut

=head1 LICENSE AND COPYRIGHT

Copyright (c) 2015 Maxim Boguk

=head1 AUTHOR

=over 4

=item L<Maxim Boguk|mailto:maxim.boguk@gmail.com>

=back

=cut
