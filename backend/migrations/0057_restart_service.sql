-- WHM-style service restart (ADR-069): restart nginx/Apache/OLS/php-fpm/DB
-- services on a managed server via the restart_service agent job.
ALTER TYPE job_type ADD VALUE IF NOT EXISTS 'restart_service';
