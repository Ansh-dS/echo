CREATE TYPE task_status as Enum (
    'created',
    'queued',
    'running',
    'waiting_for_user',
    'completed',
    'failed'
);


CREATE TABLE tasks (
    id UUID Primary Key DEFAULT gen_random_uuid(), 
    status task_status Not Null DEFAULT 'created', 
    created_at TIMESTAMPTZ Not NUll DEFAULT Now(), 
    updated_at TIMESTAMPTZ Not Null DEFAULT Now()

); 

CREATE or REPLACE 
FUNCTION update_states_check()
RETURNS TRIGGER 
AS $$
BEGIN 
    IF OLD.status = NEW.status THEN
        RETURN NEW;
    END IF;
  
    IF (OLD.status = 'created' AND NEW.status = 'queued') OR
       (OLD.status = 'queued' AND NEW.status = 'running') OR
       (OLD.status = 'running' AND NEW.status IN ('waiting_for_user', 'completed', 'failed')) OR
       (OLD.status = 'waiting_for_user' AND NEW.status IN ('running', 'completed', 'failed')) THEN
        
        NEW.updated_at = NOW();
        RETURN NEW;

    END IF;
    RAISE EXCEPTION 'Invalid task status transition from "%" to "%"', OLD.status, NEW.status;
END;
$$ LANGUAGE plpgsql

CREATE TRIGGER state_check
BEFORE UPDATE OF status ON tasks 
FOR EACH ROW
EXECUTE FUNCTION
update_states_check()
