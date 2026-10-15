-- One postgres, six databases.
--
-- The services each had their own postgres container in their own compose file,
-- which would have meant six of them in the development stack. The databases are
-- separate and the credentials are per-service, so nothing is shared here that
-- should not be — the only thing consolidated is the server process.
--
-- Runs once, on an empty data directory. Changing a password here after the
-- volume exists has no effect: `make reset` is what re-applies it.

CREATE USER gogo        WITH PASSWORD 'gogo';
CREATE DATABASE gogo        OWNER gogo;

CREATE USER category_user WITH PASSWORD 'category_pass';
CREATE DATABASE category_db OWNER category_user;

CREATE USER media_user    WITH PASSWORD 'media_pass';
CREATE DATABASE media_db    OWNER media_user;

CREATE USER product_user  WITH PASSWORD 'product_pass';
CREATE DATABASE product_db  OWNER product_user;

CREATE USER listing_user  WITH PASSWORD 'listing_pass';
CREATE DATABASE listing_db  OWNER listing_user;

CREATE USER search_user   WITH PASSWORD 'search_pass';
CREATE DATABASE search_db   OWNER search_user;
